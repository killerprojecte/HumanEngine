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
	"io"
	"testing"
	"time"
)

func TestTypingBuiltinRealWASMAndPendingCancellation(t *testing.T) {
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
	manager, err := pluginmanager.New(main, authority, host, runtime, "node", t.TempDir(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	if err = manager.WithWASM(wasm); err != nil {
		t.Fatal(err)
	}
	if err = manager.RegisterBuiltin("typing-delay"); err != nil {
		t.Fatal(err)
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
