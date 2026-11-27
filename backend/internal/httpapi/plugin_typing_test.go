//go:build bundled_wasm

package httpapi

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	typing "github.com/killerprojecte/HumanEngine/backend/plugins/typing-delay"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTypingBuiltinRealWASMAndPendingCancellation(t *testing.T) {
	testTypingWASM(t, false)
}

func TestTypingExternalRealWASMAndPendingCancellation(t *testing.T) {
	testTypingWASM(t, true)
}

func testTypingWASM(t *testing.T, external bool) {
	t.Helper()
	_, main, _, gate := fixtureServer(t)
	authority, err := recovery.OpenWriter(context.Background(), gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	host := plugins.NewV1Host("node", io.Discard)
	defer host.Close(context.Background())
	wasm, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer wasm.Close(context.Background())
	runtime := new(botruntime.Runtime)
	root := t.TempDir()
	manager, err := pluginmanager.New(main, authority, host, runtime, "node", root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if err = manager.WithWASM(wasm); err != nil {
		t.Fatal(err)
	}
	const id = "typing-delay"
	if external {
		manifestValue, err := jsjson.ParseBytes(typing.Manifest().(json.RawMessage))
		if err != nil {
			t.Fatal(err)
		}
		manifest := manifestValue.(*jsjson.Object)
		if err := manifest.Set("artifact", "typing-delay.wasm"); err != nil {
			t.Fatal(err)
		}
		wire, err := jsjson.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		entry := id + ".v2.json"
		artifact, err := typing.Artifact()
		if err != nil {
			t.Fatal(err)
		}
		for name, payload := range map[string][]byte{entry: wire, "typing-delay.wasm": artifact} {
			if err = os.WriteFile(filepath.Join(root, name), payload, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err = manager.RegisterExternalV2(entry, nil); err != nil {
			t.Fatal(err)
		}
	} else if err = manager.RegisterBuiltin(pluginmanager.BuiltinDescriptor{Manifest: typing.Manifest().(json.RawMessage), LoadArtifact: typing.Artifact, Start: manager.StartSendDelay}); err != nil {
		t.Fatal(err)
	}
	entries, err := manager.List(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected catalog: %v %v", entries, err)
	}
	initial := entries[0].(*jsjson.Object)
	enabled, _ := initial.Get("enabled")
	builtin, _ := initial.Get("builtin")
	if enabled != false || builtin != !external || wasm.Loaded(id) {
		t.Fatal("plugin not merely registered and inactive", enabled, builtin)
	}
	if external {
		bad, _ := jsjson.Parse(`{"speedFactor":0.01}`)
		if _, err = manager.Configure(context.Background(), id, bad.(*jsjson.Object), "bad-config"); err == nil {
			t.Fatal("external schema admitted out-of-range speed")
		}
	}
	config, _ := jsjson.Parse(`{"speedFactor":10}`)
	if _, err = manager.Configure(context.Background(), "typing-delay", config.(*jsjson.Object), "config"); err != nil {
		t.Fatal(err)
	}
	if err = manager.Enable(context.Background(), "typing-delay", "enable"); err != nil {
		t.Fatal(err)
	}
	if !wasm.Loaded("typing-delay") || host.PID() != 0 {
		t.Fatal("guest was not loaded without Node")
	}
	started := time.Now()
	_, failure := dispatch.AwaitSendStage(func() *dispatch.Future[struct{}] {
		return runtime.WaitTypingDelay(dispatch.Segment{Parts: []json.RawMessage{}}, dispatch.NewSignal())
	}, nil)
	if failure != nil || time.Since(started) < 20*time.Millisecond {
		t.Fatal("real wait missing", failure, time.Since(started))
	}
	config, _ = jsjson.Parse(`{"speedFactor":0.1}`)
	if _, err = manager.Configure(context.Background(), "typing-delay", config.(*jsjson.Object), "reconfigure"); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan *dispatch.Failure, 1)
	future := runtime.WaitTypingDelay(dispatch.Segment{Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"pending timer"}`)}}, dispatch.NewSignal())
	go func() {
		_, failure := dispatch.AwaitSendStage(func() *dispatch.Future[struct{}] { return future }, nil)
		waiting <- failure
	}()
	select {
	case failure := <-waiting:
		t.Fatal("long policy returned before disable", failure)
	case <-time.After(10 * time.Millisecond):
	}
	if err = manager.Disable(context.Background(), "typing-delay", "disable"); err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-waiting:
		if failure == nil {
			t.Fatal("pending delay published success after disable")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending delay not cancelled")
	}
	if wasm.Loaded("typing-delay") || host.PID() != 0 {
		t.Fatal("guest retained or Node started")
	}
	_, failure = dispatch.AwaitSendStage(func() *dispatch.Future[struct{}] {
		return runtime.WaitTypingDelay(dispatch.Segment{}, dispatch.NewSignal())
	}, nil)
	if failure != nil {
		t.Fatal("removed delay still active", failure)
	}
	list, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry := list[0].(*jsjson.Object)
	version, _ := entry.Get("apiVersion")
	if version != float64(2) {
		t.Fatal("V2 badge metadata missing", version)
	}
}
