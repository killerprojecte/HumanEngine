package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This targets the publication fence after a successful worker claim, rather
// than the earlier cancellation window while the continuation getter is busy.
func TestV1OpaqueContinuationCancelledAfterClaimBeforePublication(t *testing.T) {
	root := t.TempDir()
	started := filepath.Join(root, "claim-started")
	release := filepath.Join(root, "claim-release")
	encodedStarted, _ := json.Marshal(started)
	encodedRelease, _ := json.Marshal(release)
	host, protocol, _ := continuationHost(t, `ctx.registerModelProtocol('custom','Fixture',{async complete(){const fs=await import('node:fs');return {text:'ok',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},get continuation(){fs.writeFileSync(`+string(encodedStarted)+`,'started');while(!fs.existsSync(`+string(encodedRelease)+`)){};return new Map([['private',()=>42]])}}}})`)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		result, err := host.CompleteModelProtocol(ctx, protocol.ID, protocol.Generation, continuationRequest("claim", nil))
		if result != nil {
			_ = result.Continuation().Release()
			finished <- errors.New("cancelled publication returned a continuation owner")
			return
		}
		finished <- err
	}()
	waitContinuationMarker(t, started, finished)
	func() {
		host.lifecycle.Lock()
		defer host.lifecycle.Unlock()
		lease := host.enabled["opaque"]
		if err := os.WriteFile(release, nil, 0600); err != nil {
			t.Fatal(err)
		}
		// This reply follows the claim on the worker queue. Holding lifecycle
		// prevents CompleteModelProtocol from publishing its already-claimed ref.
		statusCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		status, err := lease.process.call(statusCtx, map[string]any{"op": "continuation_status", "plugin": lease.key})
		var counts struct{ Pending, Claimed int }
		if err != nil || json.Unmarshal(status, &counts) != nil || counts.Pending != 0 || counts.Claimed != 1 {
			t.Fatal("worker did not acknowledge claim before publication fence", string(status), err)
		}
		cancel()
	}()
	select {
	case err := <-finished:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled publication did not settle")
	}
	continuationCounts(t, host, 0, 0)
}

func TestV1LateWorkerExitCannotRetireReplacementOrSibling(t *testing.T) {
	host, _, path := continuationHost(t, `ctx.registerModelProtocol('custom','Fixture',{complete:async()=>({text:'opaque',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0},continuation:()=>42})})`)
	siblingPath := filepath.Join(t.TempDir(), "sibling.mjs")
	sibling := `export default{manifest:{id:'sibling',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Sibling',{complete:async()=>({text:'sibling',toolCalls:[],usage:{inputTokens:0,outputTokens:0,totalTokens:0}})})}}`
	if err := os.WriteFile(siblingPath, []byte(sibling), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(context.Background(), "sibling", siblingPath, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	host.lifecycle.Lock()
	old := host.enabled["opaque"]
	host.lifecycle.Unlock()
	pid := host.PID()
	if err := host.Disable(context.Background(), "opaque"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enable(context.Background(), "opaque", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	// Re-deliver the old exact worker event after its same-name successor exists.
	// The old and new leases deliberately share the same live Node process.
	host.workerExited(old.process, old.key)
	protocols := host.RegisteredModelProtocols()
	if len(protocols) != 2 || protocols[0].ID != "sibling:custom" || protocols[1].ID != "opaque:custom" || host.PID() != pid {
		t.Fatal("late worker exit affected a different generation", protocols, host.PID(), pid)
	}
	for _, protocol := range protocols {
		completeContinuation(t, host, context.Background(), protocol, continuationRequest("fresh", nil))
	}
}
