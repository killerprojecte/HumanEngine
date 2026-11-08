package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigurationOperationObjectIdentityAndExit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.mjs")
	trace := filepath.Join(root, "trace")
	source := `import {appendFileSync} from 'node:fs';let held,count=0; export default {manifest:{id:'operation',configSchema:{type:'object',additionalProperties:true}},setup(){},validateConfiguration(c){if(c===held||c.mutated!==undefined)throw Error('first_object_reused');held=c;count++;c.mutated=41;appendFileSync(c.trace,'validate:'+process.pid+'\n')},prepareConfiguration(c){if(c!==held)throw Error('object_identity_lost');appendFileSync(c.trace,'prepare:'+process.pid+'\n');return {value:c.mutated+1,pid:process.pid,count}}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"trace": trace})
	op, result, err := BeginConfiguration(context.Background(), "node", io.Discard, InspectionRequest{ID: "operation", Path: path, Config: raw})
	if err != nil || result.Failed {
		t.Fatal(result, err)
	}
	defer op.Close()
	before, _ := os.ReadFile(trace)
	if !strings.HasPrefix(string(before), "validate:") || strings.Contains(string(before), "prepare:") {
		t.Fatal(string(before))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(before), "validate:")))
	if err != nil {
		t.Fatal(err)
	}
	if err = syscall.Kill(pid, 0); err != nil {
		t.Fatal("validation process did not remain for prepare", err)
	}
	if result, err = op.Validate(json.RawMessage(`{}`)); err != nil || result.Failed {
		t.Fatal(result, err)
	}
	result, err = op.Prepare()
	if err != nil || result.Failed {
		t.Fatal(result, err)
	}
	var value struct{ Value, PID, Count int }
	if err = json.Unmarshal(result.Config, &value); err != nil || value.Value != 42 || value.PID != pid || value.Count != 2 {
		t.Fatal(string(result.Config), err)
	}
	op.Close()
	if err = syscall.Kill(pid, 0); err == nil {
		t.Fatal("operation retained Node")
	}
}

func TestConfigurationOperationFailureCancellationAndClose(t *testing.T) {
	for _, mode := range []string{"close", "validation_failure", "prepare_failure", "cancel_prepare"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "fixture.mjs")
			trace := filepath.Join(root, "trace")
			source := `import{writeFileSync,appendFileSync}from'node:fs';export default{manifest:{id:'operation',configSchema:{type:'object',additionalProperties:true}},setup(){},validateConfiguration(c){writeFileSync(c.trace,String(process.pid));if(c.mode==='validation_failure')throw Error('invalid')},prepareConfiguration(c){appendFileSync(c.trace,'\nentered');if(c.mode==='prepare_failure')throw Error('failed');if(c.mode==='cancel_prepare')while(true){};return c}}`
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw, _ := json.Marshal(map[string]any{"mode": mode, "trace": trace})
			op, result, err := BeginConfiguration(ctx, "node", io.Discard, InspectionRequest{ID: "operation", Path: path, Config: raw})
			data, _ := os.ReadFile(trace)
			pid, e := strconv.Atoi(strings.Split(string(data), "\n")[0])
			if e != nil {
				t.Fatal(string(data), e, err)
			}
			if mode == "validation_failure" {
				if err != nil || !result.Failed || op != nil {
					t.Fatal(result, err)
				}
			} else {
				if err != nil || result.Failed {
					t.Fatal(result, err)
				}
				defer op.Close()
				if mode != "close" {
					r, e := op.Validate(json.RawMessage(`{}`))
					if e != nil || r.Failed {
						t.Fatal(r, e)
					}
				}
				if mode == "prepare_failure" {
					r, e := op.Prepare()
					if e != nil || !r.Failed {
						t.Fatal(r, e)
					}
				}
				if mode == "cancel_prepare" {
					returned := make(chan error, 1)
					go func() { _, e := op.Prepare(); returned <- e }()
					timer := time.NewTimer(5 * time.Second)
					defer timer.Stop()
					ticker := time.NewTicker(time.Millisecond)
					defer ticker.Stop()
				waitEntered:
					for {
						select {
						case e := <-returned:
							t.Fatalf("prepare returned before cancellation: %v", e)
						case <-timer.C:
							t.Fatal("prepare never entered")
						case <-ticker.C:
							data, _ = os.ReadFile(trace)
							if strings.Contains(string(data), "entered") {
								break waitEntered
							}
						}
					}
					cancel()
					select {
					case e := <-returned:
						if !errors.Is(e, context.Canceled) {
							t.Fatal(e)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("cancel did not release read")
					}
				}
				op.Close()
			}
			if e := syscall.Kill(pid, 0); e == nil {
				t.Fatal("operation process still alive", mode)
			}
		})
	}
}

func TestLoadedConfigurationCancellationRetiresOnlyTarget(t *testing.T) {
	h := NewV1Host("node", io.Discard)
	defer h.Close(context.Background())
	root := t.TempDir()
	trace := filepath.Join(root, "trace")
	handles := map[string]string{}
	for _, id := range []string{"operation", "other"} {
		path := filepath.Join(root, id+".mjs")
		source := `import{writeFileSync}from'node:fs';export default{manifest:{id:'` + id + `',configSchema:{type:'object',additionalProperties:true},capabilities:[]},setup(c){c.registerAdminAction('ping',()=>42)},validateConfiguration(c){},prepareConfiguration(c){writeFileSync(c.trace,'entered');while(true){}}}`
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		raw, err := h.Enable(context.Background(), id, path, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		var loaded struct{ Registrations []struct{ Handle string } }
		if err = json.Unmarshal(raw, &loaded); err != nil {
			t.Fatal(err)
		}
		handles[id] = loaded.Registrations[0].Handle
	}
	pid := h.PID()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config, _ := json.Marshal(map[string]any{"trace": trace})
	op, result, err := h.BeginLoadedConfiguration(ctx, "operation", InspectionRequest{ID: "operation", Config: config})
	if err != nil || result.Failed {
		t.Fatal(result, err)
	}
	defer op.Close()
	if r, e := op.Validate(json.RawMessage(`{}`)); e != nil || r.Failed {
		t.Fatal(r, e)
	}
	returned := make(chan error, 1)
	go func() { _, e := op.Prepare(); returned <- e }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
entered:
	for {
		select {
		case e := <-returned:
			t.Fatalf("prepare returned early: %v", e)
		case <-timer.C:
			t.Fatal("prepare did not enter")
		case <-ticker.C:
			if b, _ := os.ReadFile(trace); string(b) == "entered" {
				break entered
			}
		}
	}
	cancel()
	select {
	case e := <-returned:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("target worker did not retire")
	}
	op.Close()
	if h.PID() != pid {
		t.Fatal("unrelated plugin lost shared host", pid, h.PID())
	}
	if _, err = h.Call(context.Background(), "operation", handles["operation"], nil); !errors.Is(err, ErrNotLoaded) {
		t.Fatal("retired generation remained callable", err)
	}
	answer, err := h.Call(context.Background(), "other", handles["other"], nil)
	if err != nil || string(answer) != "42" {
		t.Fatal(string(answer), err)
	}
	if err = h.Disable(context.Background(), "other"); err != nil || h.PID() != 0 {
		t.Fatal("last plugin retained shared Node", err, h.PID())
	}
}
