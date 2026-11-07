package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func continuationHost(t *testing.T, body string) (*V1Host, ModelProtocolMetadata, string) {
	t.Helper()
	host := NewV1Host("node", io.Discard)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	path := filepath.Join(t.TempDir(), "opaque.mjs")
	source := `export default {manifest:{id:'opaque',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){` + body + `}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(context.Background(), "opaque", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	return host, host.RegisteredModelProtocols()[0], path
}

func continuationRequest(mode string, previous *ContinuationRef) ModelCompletionRequest {
	return ModelCompletionRequest{ModelID: "model-one", Context: map[string]any{}, Messages: mode, Tools: []any{}, Previous: previous, Validate: validateCompletionFixture}
}

func completeContinuation(t *testing.T, host *V1Host, ctx context.Context, protocol ModelProtocolMetadata, request ModelCompletionRequest) *ModelCompletionResult {
	t.Helper()
	result, err := host.CompleteModelProtocol(ctx, protocol.ID, protocol.Generation, request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = result.Continuation().Release() })
	return result
}

func continuationCounts(t *testing.T, host *V1Host, pending, claimed int) {
	t.Helper()
	host.lifecycle.Lock()
	lease := host.enabled["opaque"]
	host.lifecycle.Unlock()
	if lease == nil {
		t.Fatal("fixture isolate missing")
	}
	result, err := lease.process.call(context.Background(), map[string]any{"op": "continuation_status", "plugin": lease.key})
	var counts struct{ Pending, Claimed int }
	if err != nil || json.Unmarshal(result, &counts) != nil || counts.Pending != pending || counts.Claimed != claimed {
		t.Fatalf("continuation counts %s, wanted %d pending / %d claimed: %v", result, pending, claimed, err)
	}
}

func TestV1OpaqueContinuationPreservesOriginalValuesAndReuse(t *testing.T) {
	host, protocol, _ := continuationHost(t, `
 const cycle={secret:'synthetic-private-value',toJSON(){throw Error('opaque was serialized')}};cycle.self=cycle;
 const values={map:new Map([['key',cycle]]),date:new Date('2020-01-01T00:00:00Z'),cycle,fn:()=>42,object:{cycle},symbol:Symbol('private'),bigint:2n,undefined:undefined,null:null,false:false,zero:0,string:'synthetic-private-value'};
 ctx.registerModelProtocol('custom','Fixture',{complete(context,mode,tools,previous){
  const [operation,kind]=mode.split(':');const value=values[kind];
  if(operation==='reuse' && previous!==value)throw Error('identity was lost');
  if(operation==='fresh' && previous!==undefined)throw Error('continuation crossed identity boundary');
  return {text:operation+':'+kind,toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},continuation:value};
 }});`)
	for _, kind := range []string{"map", "date", "cycle", "fn", "object", "symbol", "bigint", "undefined", "null", "false", "zero", "string"} {
		t.Run(kind, func(t *testing.T) {
			originalCtx, cancel := context.WithCancel(context.Background())
			result := completeContinuation(t, host, originalCtx, protocol, continuationRequest("fresh:"+kind, nil))
			ref := result.Continuation()
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(string(encoded), "synthetic-private") || strings.Contains(string(encoded), "continuation") {
				t.Fatal(string(encoded), err)
			}
			for _, value := range []any{ref, *ref, map[string]any{"continuation": ref}} {
				if _, err := json.Marshal(value); !errors.Is(err, errOpaqueContinuation) {
					t.Fatal("opaque reference was serialized", err)
				}
			}
			var forged ContinuationRef
			if err := json.Unmarshal([]byte(`{"invocationID":"1","modelID":"model-one"}`), &forged); err == nil {
				t.Fatal("JSON minted a continuation")
			}
			cancel() // Finishing the original turn must not revoke assistant-owned state.
			for range 2 {
				reused := completeContinuation(t, host, context.Background(), protocol, continuationRequest("reuse:"+kind, ref))
				if err := reused.Continuation().Release(); err != nil {
					t.Fatal(err)
				}
			}
			continuationCounts(t, host, 0, 1)
			copyRef := *ref
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					if err := copyRef.Release(); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			continuationCounts(t, host, 0, 0)
			fresh := completeContinuation(t, host, context.Background(), protocol, continuationRequest("fresh:"+kind, ref))
			_ = fresh.Continuation().Release()
			continuationCounts(t, host, 0, 0)
		})
	}
}

func TestV1OpaqueContinuationRejectsMismatchedOwnership(t *testing.T) {
	body := `const original={};const adapter={complete(context,mode,tools,previous){if(previous!==undefined && previous!==original)throw Error('not original');if(mode==='fresh' && previous!==undefined)throw Error('stale reference');return {text:mode,toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},continuation:original}}};ctx.registerModelProtocol('custom','Fixture',adapter);ctx.registerModelProtocol('other','Other',adapter);`
	host, protocol, path := continuationHost(t, body)
	original := completeContinuation(t, host, context.Background(), protocol, continuationRequest("fresh", nil))
	previous := original.Continuation()
	mismatch := continuationRequest("fresh", previous)
	mismatch.ModelID = "different-model"
	completeContinuation(t, host, context.Background(), protocol, mismatch)
	other := host.RegisteredModelProtocols()[1]
	completeContinuation(t, host, context.Background(), other, continuationRequest("fresh", previous))
	another, anotherProtocol, _ := continuationHost(t, body)
	completeContinuation(t, another, context.Background(), anotherProtocol, continuationRequest("fresh", previous))
	completeContinuation(t, host, context.Background(), protocol, continuationRequest("fresh", &ContinuationRef{}))
	if err := host.Disable(context.Background(), "opaque"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(context.Background(), "opaque", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	replacement := host.RegisteredModelProtocols()[0]
	completeContinuation(t, host, context.Background(), replacement, continuationRequest("fresh", previous))
	if result, err := host.CompleteModelProtocol(context.Background(), protocol.ID, protocol.Generation, continuationRequest("fresh", previous)); err != ErrNotLoaded || result != nil {
		t.Fatal("stale call reached replacement", result, err)
	}
	if err := previous.Release(); err != nil {
		t.Fatal("releasing dead-generation ref failed", err)
	}
}

func TestV1OpaqueContinuationClaimsOnlyValidatedCurrentResult(t *testing.T) {
	host, protocol, path := continuationHost(t, `
 let reads=0;ctx.registerAdminAction('reads',async()=>reads);
 ctx.registerModelProtocol('custom','Fixture',{complete(context,mode){const output={text:mode,toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},get continuation(){reads++;return new Map()}};if(mode==='invalid-json')output.usage.self=output.usage;return output}});`)
	for _, mode := range []string{"invalid-json", "validation-failed", "cancelled-validation", "disabled-validation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := continuationRequest(mode, nil)
			validationError := errors.New("fixture_validation_failed")
			request.Validate = func(raw json.RawMessage) error {
				continuationCounts(t, host, 1, 0)
				reads, err := host.Call(context.Background(), "opaque", "1", nil)
				if err != nil || string(reads) != "0" {
					t.Fatal("continuation getter ran before validation", string(reads), err)
				}
				switch mode {
				case "validation-failed":
					return validationError
				case "cancelled-validation":
					cancel()
				case "disabled-validation":
					if err := host.Disable(context.Background(), "opaque"); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			result, err := host.CompleteModelProtocol(ctx, protocol.ID, protocol.Generation, request)
			if result != nil || err == nil {
				t.Fatal("invalid/stale completion claimed continuation", result, err)
			}
			if mode == "validation-failed" && !errors.Is(err, validationError) {
				t.Fatal(err)
			}
			if mode == "cancelled-validation" && err != context.Canceled {
				t.Fatal(err)
			}
			if mode == "disabled-validation" {
				if err != ErrNotLoaded {
					t.Fatal(err)
				}
				if _, err := host.Enable(context.Background(), "opaque", path, map[string]any{}); err != nil {
					t.Fatal(err)
				}
				protocol = host.RegisteredModelProtocols()[0]
			}
			continuationCounts(t, host, 0, 0)
		})
	}
	good := continuationRequest("good", nil)
	good.Validate = func(raw json.RawMessage) error {
		continuationCounts(t, host, 1, 0)
		return validateCompletionFixture(raw)
	}
	completeContinuation(t, host, context.Background(), protocol, good)
	reads, err := host.Call(context.Background(), "opaque", "1", nil)
	if err != nil || string(reads) != "1" {
		t.Fatal("claim did not read opaque state exactly once", string(reads), err)
	}
	continuationCounts(t, host, 0, 1)
}

func TestV1OpaqueContinuationCannotBypassTypedBridge(t *testing.T) {
	host, protocol, _ := continuationHost(t, `ctx.registerModelProtocol('custom','Fixture',{complete:async()=>({text:'ok',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},continuation:()=>42})})`)
	// Raw marker objects do not prove ownership and cannot be sent as arg four.
	for _, args := range [][]any{nil, {map[string]any{}, nil, nil, map[string]any{"continuation": "1"}}} {
		if _, err := host.CallModelProtocol(context.Background(), protocol.ID, protocol.Generation, "complete", args); err != errTypedModelCompletionRequired {
			t.Fatal(err)
		}
		if _, err := host.Call(context.Background(), "opaque", "1", args); err != errTypedModelCompletionRequired {
			t.Fatal(err)
		}
	}
	request := continuationRequest("fresh", nil)
	request.Validate = nil
	if _, err := host.CompleteModelProtocol(context.Background(), protocol.ID, protocol.Generation, request); err == nil {
		t.Fatal("missing validator was accepted")
	}
	continuationCounts(t, host, 0, 0)
}

func TestV1WorkerCrashHidesOnlyExactIsolateProtocols(t *testing.T) {
	host, protocol, path := continuationHost(t, `ctx.registerAdminAction('exit',async()=>process.exit(7));ctx.registerModelProtocol('custom','Fixture',{complete:async()=>({text:'ok',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},continuation:()=>42})})`)
	siblingPath := filepath.Join(t.TempDir(), "sibling.mjs")
	if err := os.WriteFile(siblingPath, []byte(`export default{manifest:{id:'sibling',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Sibling',{complete:async()=>({text:'sibling',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0}})})}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(context.Background(), "sibling", siblingPath, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	pid := host.PID()
	old := completeContinuation(t, host, context.Background(), protocol, continuationRequest("fresh", nil))
	if _, err := host.Call(context.Background(), "opaque", "1", nil); err == nil {
		t.Fatal("crash returned success")
	}
	protocols := host.RegisteredModelProtocols()
	if len(protocols) != 1 || protocols[0].ID != "sibling:custom" || host.PID() != pid {
		t.Fatal("dead isolate remained visible or sibling host was stopped", protocols, host.PID(), pid)
	}
	completeContinuation(t, host, context.Background(), protocols[0], continuationRequest("fresh", nil))
	if err := old.Continuation().Release(); err != nil {
		t.Fatal(err)
	}
	// Re-enable waits for only the retired isolate and keeps the shared process.
	if _, err := host.Enable(context.Background(), "opaque", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if host.PID() != pid {
		t.Fatal("replacement restarted sibling's shared host")
	}
	replacement := host.RegisteredModelProtocols()[1]
	completeContinuation(t, host, context.Background(), replacement, continuationRequest("fresh", old.Continuation()))
}

func TestV1EnableConfigurationEncodingCanReenterHost(t *testing.T) {
	host := NewV1Host("node", io.Discard)
	defer host.Close(context.Background())
	path := filepath.Join(t.TempDir(), "config.mjs")
	if err := os.WriteFile(path, []byte(`export default{manifest:{id:'config',configSchema:{type:'object',additionalProperties:true}},setup(ctx){ctx.registerAdminAction('config',async()=>ctx.config)}}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := host.Enable(ctx, "config", path, map[string]any{"reentrant": metadataArgument{host}})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("configuration marshaler deadlocked lifecycle admission")
	}
	result, err := host.Call(ctx, "config", "1", nil)
	if err != nil || string(result) != `{"reentrant":{"provider":{}}}` {
		t.Fatal(string(result), err)
	}
}

func waitContinuationMarker(t *testing.T, path string, finished <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case err := <-finished:
			t.Fatal("call exited before test gate", err)
		case <-deadline.C:
			t.Fatal("call did not reach test gate")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestV1OpaqueContinuationCancellationDuringClaimReleasesLateValue(t *testing.T) {
	root := t.TempDir()
	started := filepath.Join(root, "started")
	release := filepath.Join(root, "release")
	encodedStarted, _ := json.Marshal(started)
	encodedRelease, _ := json.Marshal(release)
	host, protocol, _ := continuationHost(t, `
 ctx.registerModelProtocol('custom','Fixture',{async complete(){const fs=await import('node:fs');return {text:'ok',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},get continuation(){fs.writeFileSync(`+string(encodedStarted)+`,'started');while(!fs.existsSync(`+string(encodedRelease)+`)){};return ()=>42}}}});`)
	// Never leave a deliberately blocked fixture alive after an assertion failure.
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		result, err := host.CompleteModelProtocol(ctx, protocol.ID, protocol.Generation, continuationRequest("claim", nil))
		if result != nil {
			_ = result.Continuation().Release()
			finished <- errors.New("cancelled claim returned an owner")
		} else {
			finished <- err
		}
	}()
	waitContinuationMarker(t, started, finished)
	cancel()
	select {
	case err := <-finished:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("claim cancellation waited for uncooperative getter")
	}
	host.lifecycle.Lock()
	process := host.process
	host.lifecycle.Unlock()
	status, err := process.call(context.Background(), map[string]any{"op": "status"})
	var transport struct{ PendingRequests int }
	if err != nil || json.Unmarshal(status, &transport) != nil || transport.PendingRequests != 0 {
		t.Fatal("cancelled claim retained host RPC listener", string(status), err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	continuationCounts(t, host, 0, 0)
}

func TestV1OpaqueContinuationCancellationBeforeLateCompletionReleasesPending(t *testing.T) {
	root := t.TempDir()
	started := filepath.Join(root, "started")
	release := filepath.Join(root, "release")
	encodedStarted, _ := json.Marshal(started)
	encodedRelease, _ := json.Marshal(release)
	host, protocol, _ := continuationHost(t, `
 let finished=false;ctx.registerAdminAction('finished',async()=>finished);
 ctx.registerModelProtocol('custom','Fixture',{async complete(){const fs=await import('node:fs');fs.writeFileSync(`+string(encodedStarted)+`,'started');while(!fs.existsSync(`+string(encodedRelease)+`))await new Promise(resolve=>setTimeout(resolve,1));finished=true;return {text:'late',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},get continuation(){throw Error('cancelled result must never read opaque state')}}}});`)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		result, err := host.CompleteModelProtocol(ctx, protocol.ID, protocol.Generation, continuationRequest("late", nil))
		if result != nil {
			_ = result.Continuation().Release()
			finished <- errors.New("late completion returned an owner")
		} else {
			finished <- err
		}
	}()
	waitContinuationMarker(t, started, finished)
	cancel()
	if err := <-finished; err != context.Canceled {
		t.Fatal(err)
	}
	continuationCounts(t, host, 0, 0)
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		value, err := host.Call(context.Background(), "opaque", "1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(value) == "true" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("late callback never completed")
		case <-time.After(time.Millisecond):
		}
	}
	continuationCounts(t, host, 0, 0)
}

func TestV1OpaqueContinuationClaimFailureReleasesPending(t *testing.T) {
	host, protocol, _ := continuationHost(t, `ctx.registerModelProtocol('custom','Fixture',{complete:async()=>({text:'ok',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},get continuation(){throw Error('synthetic-private-getter')}})})`)
	result, err := host.CompleteModelProtocol(context.Background(), protocol.ID, protocol.Generation, continuationRequest("bad-getter", nil))
	if result != nil || err == nil || err.Error() != "model_protocol_failure" {
		t.Fatal("getter failed without sanitized invocation error", result, err)
	}
	continuationCounts(t, host, 0, 0)
}

func TestV1UncaughtWorkerCrashRetiresLastLeaseWithoutDisable(t *testing.T) {
	host, _, _ := continuationHost(t, `ctx.registerAdminAction('crash',async()=>{setImmediate(()=>{throw Error('synthetic-worker-crash')});return true});ctx.registerModelProtocol('custom','Fixture',{complete:async()=>({text:'ok',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0}})})`)
	host.lifecycle.Lock()
	lease := host.enabled["opaque"]
	host.lifecycle.Unlock()
	// The action's reply may win or lose its race with the asynchronous crash.
	_, _ = host.Call(context.Background(), "opaque", "1", nil)
	select {
	case <-lease.done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker crash did not retire its lease")
	}
	if host.PID() != 0 || len(host.RegisteredModelProtocols()) != 0 {
		t.Fatal("last dead isolate kept shared Node or registrations alive")
	}
	host.lifecycle.Lock()
	remaining := len(host.enabled)
	host.lifecycle.Unlock()
	if remaining != 0 {
		t.Fatal("dead worker still occupies Go lease registry", remaining)
	}
}
