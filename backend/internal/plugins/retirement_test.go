package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRetiredConfigurationLeaseDoesNotMoveToReplacement(t *testing.T) {
	h := NewV1Host("node", io.Discard)
	defer h.Close(context.Background())
	path := filepath.Join(t.TempDir(), "fixture.mjs")
	source := `let count=0,held;export default{manifest:{id:'retired',configSchema:{type:'object',additionalProperties:true},capabilities:[]},validateConfiguration(c){held=c;count++},prepareConfiguration(c){if(c!==held)throw Error('identity');return{count}},setup(c){c.registerAdminAction('count',()=>count)}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := h.Enable(context.Background(), "retired", path, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var registrations struct{ Registrations []struct{ Handle string } }
	if err = json.Unmarshal(raw, &registrations); err != nil {
		t.Fatal(err)
	}
	h.lifecycle.Lock()
	old := h.enabled["retired"]
	h.lifecycle.Unlock()
	pid := h.PID()
	a, result, err := h.BeginLoadedConfiguration(context.Background(), "retired", InspectionRequest{ID: "retired"})
	if err != nil || result.Failed {
		t.Fatal(result, err)
	}
	defer a.Close()
	b, result, err := h.BeginLoadedConfiguration(context.Background(), "retired", InspectionRequest{ID: "retired"})
	if err != nil || result.Failed {
		t.Fatal(result, err)
	}
	defer b.Close()
	if r, e := a.Validate(json.RawMessage(`{}`)); e != nil || r.Failed {
		t.Fatal(r, e)
	}
	if r, e := a.Prepare(); e != nil || r.Failed || string(r.Config) != `{"count":4}` {
		t.Fatal(string(r.Config), r, e)
	}
	a.Close()
	if err = h.RetireManagedRuntime(context.Background(), "retired"); err != nil {
		t.Fatal(err)
	}
	if h.PID() != pid {
		t.Fatal("accepted configuration lease lost process")
	}
	if _, err = h.Call(context.Background(), "retired", registrations.Registrations[0].Handle, nil); !errors.Is(err, ErrNotLoaded) {
		t.Fatal("retired ordinary call accepted", err)
	}
	if op, _, e := h.BeginLoadedConfiguration(context.Background(), "retired", InspectionRequest{ID: "retired"}); e == nil {
		op.Close()
		t.Fatal("retired isolate accepted new token")
	}
	if _, err = h.Enable(context.Background(), "retired", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if r, e := b.Validate(json.RawMessage(`{}`)); e != nil || r.Failed {
		t.Fatal(r, e)
	}
	if r, e := b.Prepare(); e != nil || r.Failed || string(r.Config) != `{"count":5}` {
		t.Fatal("token migrated or state lost", string(r.Config), e)
	}
	b.Close()
	select {
	case <-old.done:
	default:
		t.Fatal("retired worker not physically joined")
	}
	if h.PID() != pid {
		t.Fatal("replacement lost shared host")
	}
	if _, err = h.callLease(context.Background(), "retired", old, registrations.Registrations[0].Handle, nil); !errors.Is(err, ErrNotLoaded) {
		t.Fatal("old generation reached replacement", err)
	}
	if err = h.Disable(context.Background(), "retired"); err != nil || h.PID() != 0 {
		t.Fatal(err, h.PID())
	}
}

func TestRetiredConfigurationCloseAndOldCancellation(t *testing.T) {
	for _, mode := range []string{"host_close", "cancel_old"} {
		t.Run(mode, func(t *testing.T) {
			h := NewV1Host("node", io.Discard)
			defer h.Close(context.Background())
			path := filepath.Join(t.TempDir(), "fixture.mjs")
			source := `export default{manifest:{id:'retired',configSchema:{type:'object',additionalProperties:true},capabilities:[]},setup(c){c.registerAdminAction('ping',()=>42)}}`
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Enable(context.Background(), "retired", path, map[string]any{}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			op, r, err := h.BeginLoadedConfiguration(ctx, "retired", InspectionRequest{ID: "retired"})
			if err != nil || r.Failed {
				t.Fatal(r, err)
			}
			defer op.Close()
			if err = h.RetireManagedRuntime(context.Background(), "retired"); err != nil {
				t.Fatal(err)
			}
			pid := h.PID()
			if mode == "host_close" {
				if err = h.Close(context.Background()); err != nil || h.PID() != 0 {
					t.Fatal(err, h.PID())
				}
				if _, err = op.Validate(json.RawMessage(`{}`)); !errors.Is(err, ErrNotLoaded) {
					t.Fatal("closed host retained token", err)
				}
				op.Close()
				if err = syscall.Kill(pid, 0); err == nil {
					t.Fatal("Host.Close returned before physical Node exit")
				}
				return
			}
			raw, err := h.Enable(context.Background(), "retired", path, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			var registrations struct{ Registrations []struct{ Handle string } }
			if err = json.Unmarshal(raw, &registrations); err != nil {
				t.Fatal(err)
			}
			cancel()
			op.Close()
			if h.PID() != pid {
				t.Fatal("old token cancellation killed replacement")
			}
			value, err := h.Call(context.Background(), "retired", registrations.Registrations[0].Handle, nil)
			if err != nil || string(value) != "42" {
				t.Fatal(string(value), err)
			}
			if err = h.Disable(context.Background(), "retired"); err != nil || h.PID() != 0 {
				t.Fatal(err, h.PID())
			}
		})
	}
}

func TestConcurrentDisableWaitsForLastPhysicalNodeExit(t *testing.T) {
	for round := 0; round < 3; round++ {
		h := NewV1Host("node", io.Discard)
		root := t.TempDir()
		for _, id := range []string{"first", "second"} {
			source := `export default{manifest:{id:'` + id + `',configSchema:{type:'object'},capabilities:[]},setup(c){c.onDispose(()=>new Promise(resolve=>setImmediate(resolve)))}}`
			path := filepath.Join(root, id+".mjs")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Enable(context.Background(), id, path, map[string]any{}); err != nil {
				t.Fatal(err)
			}
		}
		pid := h.PID()
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, id := range []string{"first", "second"} {
			id := id
			go func() { <-start; results <- h.Disable(context.Background(), id) }()
		}
		close(start)
		for n := 0; n < 2; n++ {
			select {
			case err := <-results:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("concurrent disable did not physically drain")
			}
		}
		if h.PID() != 0 {
			t.Fatal("last detached process retained", h.PID())
		}
		if err := syscall.Kill(pid, 0); err == nil {
			t.Fatal("disable returned before Wait")
		}
		_ = h.Close(context.Background())
	}
}

// The module object and physical Wait owner survive reactivation, while handles
// and activation guards do not. Old operation cancellation cannot kill its successor.
func TestConfigurationResumeKeepsModuleAndRejectsLateRegistration(t *testing.T) {
	h := NewV1Host("node", io.Discard)
	defer h.Close(context.Background())
	root := t.TempDir()
	path := filepath.Join(root, "fixture.mjs")
	marker := filepath.Join(root, "late")
	encoded, _ := json.Marshal(marker)
	source := `import{writeFileSync}from'node:fs';let count=0,old;export default{manifest:{id:'resume',configSchema:{type:'object',additionalProperties:true},capabilities:[]},validateConfiguration(){count++},setup(c){if(old){try{old.registerAdminAction('late',()=>0);throw Error('old_registration_accepted')}catch(e){if(e.message!=='plugin_unloaded')throw e;writeFileSync(` + string(encoded) + `,'rejected')}}old=c;c.registerAdminAction('count',()=>count)}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := h.EnableManagedJSON(context.Background(), "resume", path, json.RawMessage(`{}`), "initial", func(json.RawMessage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var before struct{ Registrations []struct{ Handle string } }
	if err = json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	h.lifecycle.Lock()
	old := h.enabled["resume"]
	h.lifecycle.Unlock()
	pid := h.PID()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op, r, err := h.BeginCurrentConfiguration(ctx, "resume", InspectionRequest{ID: "resume"})
	if err != nil || r.Failed {
		t.Fatal(r, err)
	}
	defer op.Close()
	if r, e := op.Validate(json.RawMessage(`{}`)); e != nil || r.Failed {
		t.Fatal(r, e)
	}
	if r, e := op.Prepare(); e != nil || r.Failed {
		t.Fatal(r, e)
	}
	if err = h.RetireManagedRuntime(context.Background(), "resume"); err != nil {
		t.Fatal(err)
	}
	raw, err = op.Resume(context.Background(), json.RawMessage(`{}`), "replacement", func(json.RawMessage) error { return nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	op.Close()
	h.lifecycle.Lock()
	fresh := h.enabled["resume"]
	h.lifecycle.Unlock()
	if fresh == old || fresh.generation == old.generation || fresh.moduleLifetime != old.moduleLifetime || fresh.key != old.key {
		t.Fatal("activation/module ownership not separated")
	}
	select {
	case <-old.done:
		t.Fatal("transfer fabricated physical exit")
	default:
	}
	if b, e := os.ReadFile(marker); e != nil || string(b) != "rejected" {
		t.Fatal(string(b), e)
	}
	var after struct{ Registrations []struct{ Handle string } }
	if err = json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Registrations) != 1 || after.Registrations[0].Handle == before.Registrations[0].Handle {
		t.Fatal("handle reused or stale registration retained", string(raw))
	}
	if _, err = h.callLease(context.Background(), "resume", old, before.Registrations[0].Handle, nil); !errors.Is(err, ErrNotLoaded) {
		t.Fatal("old activation invoked", err)
	}
	value, err := h.Call(context.Background(), "resume", after.Registrations[0].Handle, nil)
	if err != nil || string(value) != "4" || h.PID() != pid {
		t.Fatal(string(value), err, h.PID())
	}
	if err = h.Disable(context.Background(), "resume"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.done:
	default:
		t.Fatal("old physical waiter not completed")
	}
	if h.PID() != 0 || syscall.Kill(pid, 0) == nil {
		t.Fatal("last module not physically joined")
	}
}
