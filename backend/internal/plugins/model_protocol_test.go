package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV1ModelProtocolBridgeAndGeneration(t *testing.T) {
	ctx := context.Background()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "model.mjs")
	source := `export default{manifest:{id:'model',configSchema:{type:'object',properties:{},additionalProperties:false},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Fixture',{marker:'bound',async complete(context,messages,tools,continuation){if(this.marker!=='bound'||!context.signal)throw Error('missing protocol context');context.provider.name='changed in isolate';return{text:messages[0].content,toolCalls:[],usage:{inputTokens:1,outputTokens:2,totalTokens:3},continuation}},async embeddings(context,inputs){return{vectors:inputs.map(()=>[1,2]),usage:{inputTokens:1,outputTokens:0}}},async listModels(){return['fixture-model']}})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "model", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	protocols := host.RegisteredModelProtocols()
	if len(protocols) != 1 || protocols[0].ID != "model:custom" || len(protocols[0].Operations) != 3 {
		t.Fatal(protocols)
	}
	provider := map[string]any{"name": "original"}
	contextValue := map[string]any{"provider": provider, "credential": "synthetic-only"}
	result, err := callProtocolFixture(host, ctx, "model:custom", protocols[0].Generation, "complete", []any{contextValue, []any{map[string]any{"content": "hello"}}, []any{}})
	var decoded map[string]any
	_ = json.Unmarshal(result, &decoded)
	if err != nil || decoded["text"] != "hello" || provider["name"] != "original" {
		t.Fatal(string(result), err, provider)
	}
	if result, err = callProtocolFixture(host, ctx, "model:custom", protocols[0].Generation, "listModels", []any{contextValue}); err != nil || string(result) != `["fixture-model"]` {
		t.Fatal(string(result), err)
	}
	if result, err = callProtocolFixture(host, ctx, "model:custom", protocols[0].Generation, "embeddings", []any{contextValue, []string{"input"}}); err != nil {
		t.Fatal(string(result), err)
	}
	if err = host.Disable(ctx, "model"); err != nil {
		t.Fatal(err)
	}
	if len(host.RegisteredModelProtocols()) != 0 || host.PID() != 0 {
		t.Fatal("disabled protocol remained loaded")
	}
	if _, err = host.Enable(ctx, "model", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err = callProtocolFixture(host, ctx, "model:custom", protocols[0].Generation, "complete", nil); err != ErrNotLoaded {
		t.Fatal("stale generation reached replacement", err)
	}
}

func TestV1ConfigVocabularyRejectsInvalidBeforeSetup(t *testing.T) {
	ctx := context.Background()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "config.mjs")
	source := `export default{manifest:{id:'config',configSchema:{type:'object',properties:{speed:{type:'number',minimum:1}},required:['speed'],additionalProperties:false},capabilities:[]},setup(){throw Error('setup should not run for invalid configuration')}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		config map[string]any
		code   string
	}{{map[string]any{}, "required_property"}, {map[string]any{"speed": 0}, "expected_number"}, {map[string]any{"speed": 2, "unknown": true}, "unknown_property"}} {
		if _, err := host.Enable(ctx, "config", path, item.config); err == nil || !strings.Contains(err.Error(), item.code) {
			t.Fatal("configuration did not fail at validation", err)
		}
		if host.PID() != 0 {
			t.Fatal("failed validation retained Node")
		}
	}
}

// A caller-owned encoder can re-enter metadata APIs. It must never run under
// the host lifecycle mutex (large encodings must not fence unload either).
type metadataArgument struct{ host *V1Host }

func (a metadataArgument) MarshalJSON() ([]byte, error) {
	_ = a.host.RegisteredModelProtocols()
	return []byte(`{"provider":{}}`), nil
}

func TestV1ModelErrorsAndArgumentEncoding(t *testing.T) {
	ctx := context.Background()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "model.mjs")
	source := `export default{manifest:{id:'model',configSchema:{type:'object',properties:{}},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Fixture',{complete(){throw Error('private-provider-secret')}})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "model", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	protocol := host.RegisteredModelProtocols()[0]
	metadata, err := json.Marshal(protocol)
	if err != nil || strings.Contains(string(metadata), "generation") || strings.Contains(string(metadata), "handle") {
		t.Fatal(string(metadata), err)
	}
	_, err = callProtocolFixture(host, ctx, protocol.ID, protocol.Generation, "complete", []any{metadataArgument{host}})
	if err == nil || err.Error() != "model_protocol_failure" {
		t.Fatal("public error leaked plugin details", err)
	}
	if _, err = callProtocolFixture(host, ctx, protocol.ID, protocol.Generation, "embeddings", nil); err == nil || err.Error() != "protocol_operation_unsupported" {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = callProtocolFixture(host, cancelled, protocol.ID, protocol.Generation, "complete", nil); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestV1ModelProtocolThrownValuesRemainFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "throws.mjs")
	source := `export default{manifest:{id:'throws',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Fixture',{complete(_,mode){switch(mode){case 'empty-error':throw Error('');case 'string':throw 'synthetic-private-detail';case 'undefined':throw undefined;case 'null':throw null;case 'empty-string':throw '';case 'zero':throw 0;case 'false':throw false;case 'getter':throw {get message(){throw Error('synthetic-getter-detail')}};case 'nonstring-message':throw {message:7};case 'retry-text':throw Error('provider_http_503');default:return {text:'ordinary result data',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},failed:true,error:'ordinary result data'}}}})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "throws", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	protocol := host.RegisteredModelProtocols()[0]
	for _, mode := range []string{"empty-error", "string", "undefined", "null", "empty-string", "zero", "false", "getter", "nonstring-message", "retry-text"} {
		t.Run(mode, func(t *testing.T) {
			result, err := callProtocolFixture(host, ctx, protocol.ID, protocol.Generation, "complete", []any{map[string]any{}, mode})
			var invocationError *ProtocolInvocationError
			if result != nil || !errors.As(err, &invocationError) || err.Error() != "model_protocol_failure" {
				t.Fatalf("throw became success or escaped failure classification: %s %v", result, err)
			}
		})
	}
	result, err := callProtocolFixture(host, ctx, protocol.ID, protocol.Generation, "complete", []any{map[string]any{}, "success"})
	if err != nil || string(result) != `{"text":"ordinary result data","toolCalls":[],"usage":{"inputTokens":0,"outputTokens":0,"totalTokens":0}}` {
		t.Fatal("transport failure flag collided with result fields or a throw killed the worker", string(result), err)
	}
}

func TestV1StartupThrownValuesFailAndUnload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "startup.mjs")
	for _, phase := range []string{"validateConfiguration", "setup"} {
		for _, thrown := range []string{`Error('')`, `undefined`, `null`, `''`, `({get message(){throw Error('synthetic-getter-detail')}})`} {
			t.Run(phase+"/"+thrown, func(t *testing.T) {
				body := `setup(){},` + phase + `(){throw ` + thrown + `}`
				source := `export default{manifest:{id:'startup',configSchema:{type:'object'},capabilities:[]},` + body + `}`
				if err := os.WriteFile(path, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := host.Enable(ctx, "startup", path, map[string]any{}); err == nil || ctx.Err() != nil {
					t.Fatal("startup throw did not settle as a failure", err)
				}
				if host.PID() != 0 {
					t.Fatal("failed startup retained Node")
				}
			})
		}
	}
}

func TestV1ValidationMutationDoesNotChangeSetupConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "mutation.mjs")
	source := `export default{manifest:{id:'mutation',configSchema:{type:'object',properties:{nested:{type:'object',properties:{value:{type:'string',enum:['validated']}}}}},capabilities:[]},validateConfiguration(config){config.nested.value='validated'},setup(ctx){if(ctx.config.nested.value!=='stored')throw Error('validator mutation reached setup');ctx.registerAdminAction('config',async()=>ctx.config)}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "mutation", path, map[string]any{"nested": map[string]any{"value": "stored"}}); err != nil {
		t.Fatal(err)
	}
	result, err := host.Call(ctx, "mutation", "1", nil)
	if err != nil || string(result) != `{"nested":{"value":"stored"}}` {
		t.Fatal(string(result), err)
	}
}

func TestV1ModelProtocolSnapshotsAfterSetup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "snapshot.mjs")
	source := `export default{manifest:{id:'snapshot',configSchema:{type:'object'},capabilities:['model-protocol']},async setup(ctx){const protocol={marker:'bound',complete(){return 'before setup completed'}};ctx.registerModelProtocol('custom','Fixture',protocol);ctx.registerAdminAction('mutate',async()=>{protocol.complete=()=> 'after registration';protocol.embeddings=undefined;return true});await Promise.resolve();protocol.complete=function(){return {text:this.marker,toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0}}};protocol.embeddings=function(){return this.marker}}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "snapshot", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	protocol := host.RegisteredModelProtocols()[0]
	if strings.Join(protocol.Operations, ",") != "complete,embeddings" {
		t.Fatal("capabilities were captured before setup finished", protocol)
	}
	if _, err := host.Call(ctx, "snapshot", "1", nil); err != nil {
		t.Fatal(err)
	}
	for _, operation := range protocol.Operations {
		result, err := callProtocolFixture(host, ctx, protocol.ID, protocol.Generation, operation, []any{map[string]any{}})
		if err != nil || (operation == "embeddings" && string(result) != `"bound"`) || (operation == "complete" && !strings.Contains(string(result), `"text":"bound"`)) {
			t.Fatal("method was not snapshotted after setup with its original receiver", operation, string(result), err)
		}
	}
}

func TestV1DisposeThrownValueDoesNotSkipRemainingCleanups(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	root := t.TempDir()
	path := filepath.Join(root, "cleanup.mjs")
	marker := filepath.Join(root, "cleaned")
	encodedMarker, _ := json.Marshal(marker)
	source := `import{writeFileSync}from'node:fs';export default{manifest:{id:'cleanup',configSchema:{type:'object'},capabilities:[]},setup(ctx){ctx.onDispose(()=>writeFileSync(` + string(encodedMarker) + `,'done'));ctx.onDispose(()=>{throw null})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "cleanup", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := host.Disable(ctx, "cleanup"); err != nil {
		t.Fatal(err)
	}
	if value, err := os.ReadFile(marker); err != nil || string(value) != "done" {
		t.Fatal("throw skipped remaining cleanup", string(value), err)
	}
}

func TestV1ModelProtocolRegistrationOrderFollowsCompletedSetup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	root := t.TempDir()
	started := filepath.Join(root, "started")
	release := filepath.Join(root, "release")
	encodedStarted, _ := json.Marshal(started)
	encodedRelease, _ := json.Marshal(release)
	slowPath := filepath.Join(root, "slow.mjs")
	fastPath := filepath.Join(root, "fast.mjs")
	slow := `import{writeFileSync,existsSync}from'node:fs';export default{manifest:{id:'slow',configSchema:{type:'object'},capabilities:['model-protocol']},async setup(ctx){ctx.registerModelProtocol('custom','Slow',{complete:async()=>true});writeFileSync(` + string(encodedStarted) + `,'started');while(!existsSync(` + string(encodedRelease) + `)){if(ctx.signal.aborted)return;await new Promise(resolve=>setTimeout(resolve,1))}}}`
	fast := `export default{manifest:{id:'fast',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Fast',{complete:async()=>true})}}`
	for path, source := range map[string]string{slowPath: slow, fastPath: fast} {
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	slowDone := make(chan error, 1)
	go func() {
		_, err := host.Enable(ctx, "slow", slowPath, map[string]any{})
		slowDone <- err
	}()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-slowDone:
			t.Fatal("slow setup exited before reaching its gate", err)
		case <-ctx.Done():
			t.Fatal("slow setup did not reach its gate", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := host.Enable(ctx, "fast", fastPath, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-slowDone; err != nil {
		t.Fatal(err)
	}
	protocols := host.RegisteredModelProtocols()
	if len(protocols) != 2 || protocols[0].ID != "fast:custom" || protocols[1].ID != "slow:custom" {
		t.Fatal("model registry used enable admission order", protocols)
	}
	if protocols[0].Generation <= protocols[1].Generation {
		t.Fatal("registration order changed the lease generation identities", protocols)
	}
}

func TestV1ModelProtocolRegistrationValidatesFinalDefinition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "definition.mjs")
	for _, fixture := range []struct {
		name, capabilities, setup string
		valid                     bool
	}{
		{"completed-during-setup", `['model-protocol']`, `const p={};ctx.registerModelProtocol('custom','Fixture',p);await Promise.resolve();p.complete=async()=>true`, true},
		{"removed-during-setup", `['model-protocol']`, `const p={complete:async()=>true};ctx.registerModelProtocol('custom','Fixture',p);await Promise.resolve();delete p.complete`, false},
		{"invalid-optional-method", `['model-protocol']`, `ctx.registerModelProtocol('custom','Fixture',{complete:async()=>true,embeddings:1})`, false},
		{"missing-capability", `[]`, `ctx.registerModelProtocol('custom','Fixture',{complete:async()=>true})`, false},
		{"duplicate-name", `['model-protocol']`, `const p={complete:async()=>true};ctx.registerModelProtocol('custom','Fixture',p);ctx.registerModelProtocol('custom','Fixture',p)`, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			source := `export default{manifest:{id:'definition',configSchema:{type:'object'},capabilities:` + fixture.capabilities + `},async setup(ctx){` + fixture.setup + `}}`
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := host.Enable(ctx, "definition", path, map[string]any{})
			if (err == nil) != fixture.valid || ctx.Err() != nil {
				t.Fatal("incorrect registration acceptance", err)
			}
			if fixture.valid {
				if err := host.Disable(ctx, "definition"); err != nil {
					t.Fatal(err)
				}
			}
			if host.PID() != 0 || len(host.RegisteredModelProtocols()) != 0 {
				t.Fatal("definition failure or disable retained registrations")
			}
		})
	}
}

type reloadProtocolArgument struct {
	host *V1Host
	path string
}

func (a reloadProtocolArgument) MarshalJSON() ([]byte, error) {
	if err := a.host.Disable(context.Background(), "reload"); err != nil {
		return nil, err
	}
	if _, err := a.host.Enable(context.Background(), "reload", a.path, map[string]any{}); err != nil {
		return nil, err
	}
	return []byte(`{"provider":{}}`), nil
}

func TestV1ModelProtocolRechecksGenerationAfterArgumentEncoding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	path := filepath.Join(t.TempDir(), "reload.mjs")
	source := `export default{manifest:{id:'reload',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){let count=0;ctx.registerModelProtocol('custom','Fixture',{complete:async()=>({text:String(++count),toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0}})})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "reload", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	old := host.RegisteredModelProtocols()[0]
	if result, err := callProtocolFixture(host, ctx, old.ID, old.Generation, "complete", []any{reloadProtocolArgument{host, path}}); err != ErrNotLoaded || result != nil {
		t.Fatal("stale call was admitted after argument encoding reloaded its plugin", string(result), err)
	}
	current := host.RegisteredModelProtocols()[0]
	if result, err := callProtocolFixture(host, ctx, current.ID, current.Generation, "complete", []any{map[string]any{}}); err != nil || !strings.Contains(string(result), `"text":"1"`) {
		t.Fatal("stale call reached replacement generation", string(result), err)
	}
}

func TestV1ModelProtocolCancellationDoesNotStopConcurrentCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host := NewV1Host("node", io.Discard)
	defer host.Close(ctx)
	root := t.TempDir()
	path := filepath.Join(root, "cancel.mjs")
	started := filepath.Join(root, "started")
	encodedStarted, _ := json.Marshal(started)
	source := `import{writeFileSync}from'node:fs';export default{manifest:{id:'cancel',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Fixture',{async complete(context,wait){if(!wait)return {text:'fast',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0}};writeFileSync(` + string(encodedStarted) + `,'started');await new Promise(resolve=>context.signal.addEventListener('abort',resolve,{once:true}));return 'cancelled result must not escape'}})}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(ctx, "cancel", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	protocol := host.RegisteredModelProtocols()[0]
	waitCtx, stopWait := context.WithCancel(ctx)
	defer stopWait()
	finished := make(chan error, 1)
	go func() {
		result, err := callProtocolFixture(host, waitCtx, protocol.ID, protocol.Generation, "complete", []any{map[string]any{}, true})
		if result != nil {
			finished <- errors.New("cancelled result escaped")
			return
		}
		finished <- err
	}()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-finished:
			t.Fatal("model call exited before cancellation", err)
		case <-ctx.Done():
			t.Fatal("model call did not begin", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if result, err := callProtocolFixture(host, ctx, protocol.ID, protocol.Generation, "complete", []any{map[string]any{}, false}); err != nil || !strings.Contains(string(result), `"text":"fast"`) {
		t.Fatal("unrelated concurrent model call was stopped", string(result), err)
	}
	stopWait()
	if err := <-finished; err != context.Canceled {
		t.Fatal("model cancellation lost caller error identity", err)
	}
	if result, err := callProtocolFixture(host, ctx, protocol.ID, protocol.Generation, "complete", []any{map[string]any{}, false}); err != nil || !strings.Contains(string(result), `"text":"fast"`) {
		t.Fatal("model cancellation stopped its isolate", string(result), err)
	}
}

// Existing bridge/transport tests use this helper to keep completion calls on the
// typed boundary and release the synthetic assistant immediately after inspection.
func callProtocolFixture(host *V1Host, ctx context.Context, id string, generation uint64, operation string, args []any) (json.RawMessage, error) {
	if operation != "complete" {
		return host.CallModelProtocol(ctx, id, generation, operation, args)
	}
	request := ModelCompletionRequest{ModelID: "fixture", Validate: validateCompletionFixture}
	if len(args) > 0 {
		request.Context = args[0]
	}
	if len(args) > 1 {
		request.Messages = args[1]
	}
	if len(args) > 2 {
		request.Tools = args[2]
	}
	result, err := host.CompleteModelProtocol(ctx, id, generation, request)
	if err != nil {
		return nil, err
	}
	defer result.Continuation().Release()
	return result.Visible, nil
}

func validateCompletionFixture(raw json.RawMessage) error {
	var result struct {
		Text      *string
		ToolCalls []json.RawMessage
		Usage     map[string]any
	}
	if json.Unmarshal(raw, &result) != nil || result.Text == nil || result.ToolCalls == nil || result.Usage == nil {
		return errors.New("invalid_provider_response")
	}
	return nil
}
