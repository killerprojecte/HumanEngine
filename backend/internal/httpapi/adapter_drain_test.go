//go:build linux

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/v1adapterruntime"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A real native media RPC is deliberately blocked in a real FIFO read. Host
// cancellation/worker termination cannot make that filesystem call disappear.
func TestV1BridgeCloseWaitsActualNativeMediaCallback(t *testing.T) {
	_, db, engine := memeHTTP(t)
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal(e)
	}
	bridge, e := v1adapterruntime.New(engine, node, nil, plugins.V1HostOptions{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { bridge.CloseAndWait(context.Background()) })
	path, e := filepath.Abs("testdata/adapter-runtime.mjs")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := bridge.Host.Enable(context.Background(), "runtime-fixture", path, map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
	if e != nil {
		t.Fatal(e)
	}
	var loaded struct {
		Registrations []struct{ Kind, Name, Handle string }
	}
	if e = json.Unmarshal(raw, &loaded); e != nil {
		t.Fatal(e)
	}
	actions := map[string]string{}
	for _, r := range loaded.Registrations {
		if r.Kind == "action" {
			actions[r.Name] = r.Handle
		}
	}
	raw, e = bridge.Host.Call(context.Background(), "runtime-fixture", actions["inspect"], nil)
	if e != nil {
		t.Fatal(e)
	}
	var state struct {
		Scene  dispatch.SceneRef
		Source string
	}
	if e = json.Unmarshal(raw, &state); e != nil {
		t.Fatal(e)
	}
	var sceneValue map[string]any
	var sceneRaw string
	if e = db.QueryRow("SELECT value FROM scenes WHERE id=?", state.Scene.ID).Scan(&sceneRaw); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal([]byte(sceneRaw), &sceneValue); e != nil {
		t.Fatal(e)
	}
	sceneValue["learningEnabled"] = true
	raw, e = json.Marshal(sceneValue)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE scenes SET value=? WHERE id=?", string(raw), state.Scene.ID); e != nil {
		t.Fatal(e)
	}
	if e = engine.Runtime.RegisterScene(botruntime.Scene{SceneRef: state.Scene, LearningEnabled: true}); e != nil {
		t.Fatal(e)
	}
	var bytesPNG bytes.Buffer
	if e = png.Encode(&bytesPNG, image.NewNRGBA(image.Rect(0, 0, 3, 3))); e != nil {
		t.Fatal(e)
	}
	root, release, e := engine.RootContext(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	asset, e := engine.Import(root, state.Scene.ID, state.Source, bytesPNG.Bytes())
	if e != nil {
		release()
		t.Fatal(e)
	}
	id := memeString(memeGet(asset, "id"))
	tags := memeObject("ocr", "")
	for _, name := range []string{"appearance", "style", "subject", "emotion", "intent", "tone", "intensity", "memeMeaning"} {
		tags.Set(name, []any{})
	}
	if _, e = engine.Memes.Update(root, id, memeObject("tags", tags, "available", true)); e != nil {
		release()
		t.Fatal(e)
	}
	original, e := engine.Memes.Original(root, memeservice.Context{SceneID: state.Scene.ID}, id)
	release()
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(original.Path); e != nil {
		t.Fatal(e)
	}
	if e = syscall.Mkfifo(original.Path, 0600); e != nil {
		t.Fatal(e)
	}
	fifo, e := os.OpenFile(original.Path, os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { fifo.Close(); os.Remove(original.Path); os.WriteFile(original.Path, bytesPNG.Bytes(), 0600) })
	callDone := make(chan error, 1)
	go func() {
		_, e := bridge.Host.Call(context.Background(), "runtime-fixture", actions["media"], []any{id})
		callDone <- e
	}()
	deadline := time.After(5 * time.Second)
	for {
		entries, e := os.ReadDir("/proc/self/fd")
		if e != nil {
			t.Fatal(e)
		}
		n := 0
		for _, entry := range entries {
			target, _ := os.Readlink("/proc/self/fd/" + entry.Name())
			if target == original.Path {
				n++
			}
		}
		if n >= 2 {
			break
		}
		select {
		case e := <-callDone:
			t.Fatalf("callback ended before FIFO read: %v", e)
		case <-deadline:
			t.Fatal("real callback never entered FIFO read")
		case <-time.After(time.Millisecond):
		}
	}
	hostPID := bridge.Host.PID()
	if hostPID <= 0 {
		t.Fatal("expected actual live Node host")
	}
	closed := make(chan error, 1)
	go func() { closed <- bridge.CloseAndWait(context.Background()) }()
	awaitAdapterHostExit(t, bridge, hostPID, closed)
	select {
	case e := <-closed:
		t.Fatalf("close escaped live native callback: %v", e)
	case <-time.After(30 * time.Millisecond):
	}
	if _, e = fifo.Write(bytesPNG.Bytes()); e != nil {
		t.Fatal(e)
	}
	fifo.Close()
	select {
	case e = <-closed:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish after callback cleanup")
	}
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("plugin call did not settle")
	}
	if bridge.Host.PID() != 0 {
		t.Fatal("host process still live")
	}
	if e = bridge.CloseAndWait(context.Background()); e != nil {
		t.Fatal("idempotent close", e)
	}
}

// PID=0 alone can mean the host pointer was detached before process.wait.
// Observe the actual OS process gone as well, while the native callback remains
// blocked in FIFO/SQLite and no release action has yet occurred.
func awaitAdapterHostExit(t *testing.T, bridge *v1adapterruntime.Bridge, pid int, closed <-chan error) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		_, e := os.Stat("/proc/" + strconv.Itoa(pid))
		if os.IsNotExist(e) && bridge.Host.PID() == 0 {
			return
		}
		select {
		case e := <-closed:
			t.Fatalf("Bridge finished before blocked callback cleanup: %v", e)
		case <-deadline:
			t.Fatal("actual host process did not exit before callback release")
		case <-time.After(time.Millisecond):
		}
	}
}
