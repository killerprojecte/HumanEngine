package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestExplicitInspectionHooksSecretsAndProcessExit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.mjs")
	pidFile := filepath.Join(root, "pid")
	encoded, _ := json.Marshal(pidFile)
	source := `import{writeFileSync}from'node:fs';writeFileSync(` + string(encoded) + `,String(process.pid));setInterval(()=>{},1000);let count=0;export default{manifest:{id:'configuration',configSchema:{type:'object',properties:{secret:{type:'string',secret:true},value:{type:'number'}},required:['secret']},capabilities:[]},setup(){throw Error('setup must not execute')},validateConfiguration(config){return {...config,value:Number(config.value)}},prepareConfiguration(config){return {...config,count:++count}},configuredSecrets(){return{token:true}}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	input := InspectionRequest{Operation: "prepare", ID: "configuration", Path: path, Config: json.RawMessage(`{"value":"2"}`), Previous: json.RawMessage(`{"secret":"PRIVATE"}`)}
	for i := 0; i < 2; i++ {
		result, err := InspectV1(context.Background(), "node", io.Discard, input)
		if err != nil || result.Failed {
			t.Fatal(result, err)
		}
		if string(result.Config) != `{"value":"2","secret":"PRIVATE","count":1}` || string(result.Redacted) != `{"value":"2","count":1}` {
			t.Fatal(string(result.Config), string(result.Redacted), result.ConfiguredSecrets)
		}
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pid, _ := strconv.Atoi(string(raw))
		if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			t.Fatal("one-shot process survived", pid)
		}
	}
	// The repeated count=1 above is the documented disabled-module-state boundary.
	input.Previous = json.RawMessage(`{}`)
	result, err := InspectV1(context.Background(), "node", io.Discard, input)
	if err != nil || !result.Failed || !result.Configuration {
		t.Fatal(result, err)
	}
}
func TestExplicitInspectionCancellationWaitsExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.mjs")
	pidFile := path + ".pid"
	encoded, _ := json.Marshal(pidFile)
	if err := os.WriteFile(path, []byte(`import{writeFileSync}from'node:fs';writeFileSync(`+string(encoded)+`,String(process.pid));setInterval(()=>{},1000);await new Promise(()=>{});`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := InspectV1(ctx, "node", io.Discard, InspectionRequest{Operation: "manifest", Path: path})
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	var pid int
	for pid == 0 {
		if raw, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(string(raw))
		}
		if pid == 0 {
			select {
			case <-deadline:
				t.Fatal("inspector did not start")
			case <-time.After(time.Millisecond):
			}
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not finish")
	}
	if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		t.Fatal("cancelled inspector still alive")
	}
}
func TestEnabledInspectionRetainsModuleClosure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.mjs")
	if err := os.WriteFile(path, []byte(`let count=0;export default{manifest:{id:'state',configSchema:{type:'object',additionalProperties:true},capabilities:[]},setup(){count=10},prepareConfiguration(config){return{...config,count:++count}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewV1Host("node", io.Discard)
	defer h.Close(context.Background())
	if _, err := h.Enable(context.Background(), "state", path, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`{"count":11}`, `{"count":12}`} {
		result, err := h.InspectLoadedV1(context.Background(), "state", InspectionRequest{Operation: "prepare", ID: "state", Config: json.RawMessage(`{}`), Previous: json.RawMessage(`{}`)})
		if err != nil || string(result.Config) != want {
			t.Fatal(result, err)
		}
	}
}
