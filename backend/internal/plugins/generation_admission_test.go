package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCallbackGenerationAndCancelledToolAdmission(t *testing.T) {
	h := NewV1Host("node", io.Discard)
	defer h.Close(context.Background())
	path := filepath.Join(t.TempDir(), "generation.mjs")
	source := `export default{manifest:{id:'generation',capabilities:[],configSchema:{type:'object'}},setup(c){let calls=0;c.registerTool({name:'generation.read',effect:'read',inputSchema:{type:'object'},execute(){return ++calls}});c.registerAdminAction('run',()=>++calls);c.beforeSend({handler(){++calls;return{action:'continue'}}});c.on('runtime.error',()=>++calls);c.registerAdminAction('count',()=>calls)}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	enable := func() (uint64, []string) {
		t.Helper()
		raw, err := h.Enable(context.Background(), "generation", path, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		var ready struct{ Registrations []struct{ Handle string } }
		if err = json.Unmarshal(raw, &ready); err != nil {
			t.Fatal(err)
		}
		handles := []string{}
		for _, entry := range ready.Registrations {
			handles = append(handles, entry.Handle)
		}
		generation, ok := h.RuntimeGeneration("generation")
		if !ok {
			t.Fatal("missing generation")
		}
		return generation, handles
	}
	oldGeneration, oldHandles := enable()
	if err := h.Disable(context.Background(), "generation"); err != nil {
		t.Fatal(err)
	}
	generation, handles := enable()
	if generation == oldGeneration {
		t.Fatal("generation reused")
	}
	for index := range handles {
		if handles[index] != oldHandles[index] {
			t.Fatal("fixture must reuse handle numbers")
		}
	}
	if _, err := h.CallToolGeneration(context.Background(), "generation", oldGeneration, oldHandles[0], []any{map[string]any{}, map[string]any{}}, ToolServices{}); !errors.Is(err, ErrNotLoaded) {
		t.Fatal("old tool admitted", err)
	}
	for _, index := range []int{1, 2, 3} {
		if _, err := h.CallGeneration(context.Background(), "generation", oldGeneration, oldHandles[index], nil); !errors.Is(err, ErrNotLoaded) {
			t.Fatal("old callback admitted", index, err)
		}
	}
	h.lifecycle.Lock()
	process := h.enabled["generation"].process
	h.lifecycle.Unlock()
	process.mu.Lock()
	before := process.next
	process.mu.Unlock()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, exact := range []bool{false, true} {
		var err error
		if exact {
			_, err = h.CallToolGeneration(cancelled, "generation", generation, handles[0], nil, ToolServices{})
		} else {
			_, err = h.CallTool(cancelled, "generation", handles[0], nil, ToolServices{})
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled tool admitted", err)
		}
	}
	process.mu.Lock()
	after := process.next
	process.mu.Unlock()
	if after != before {
		t.Fatal("cancelled calls sent protocol requests", before, after)
	}
	raw, err := h.CallGeneration(context.Background(), "generation", generation, handles[4], nil)
	if err != nil || string(raw) != "0" {
		t.Fatal("rejected invocation executed side effects", string(raw), err)
	}
	raw, err = h.CallToolGeneration(context.Background(), "generation", generation, handles[0], []any{map[string]any{}, map[string]any{}}, ToolServices{})
	if err != nil || string(raw) != "1" {
		t.Fatal("new tool unavailable", string(raw), err)
	}
	if err = h.Disable(context.Background(), "generation"); err != nil || h.PID() != 0 {
		t.Fatal(err, h.PID())
	}
}
