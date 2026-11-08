package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func actionHandle(t *testing.T, raw json.RawMessage, name string) string {
	t.Helper()
	var v struct {
		Registrations []struct{ Kind, Name, Handle string }
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Registrations {
		if r.Kind == "action" && r.Name == name {
			return r.Handle
		}
	}
	t.Fatal("missing action", name)
	return ""
}

func TestQueuedAdapterEventCannotEnterReplacementActivation(t *testing.T) {
	var received atomic.Int32
	h, err := NewV1HostWithOptions("node", io.Discard, V1HostOptions{AdapterServices: AdapterServices{
		IsRecalled: func(context.Context, AdapterMetadata, string, string) (bool, error) { return false, nil },
		Receive: func(context.Context, AdapterMetadata, dispatch.SceneRef, json.RawMessage) error {
			received.Add(1)
			return nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(context.Background())
	// Delay a real wire event before native admission, not a fabricated payload.
	var delay atomic.Bool
	queued := make(chan nodeReply, 1)
	process, err := startNode(context.Background(), "node", io.Discard, h.workerExited, h.moduleReady, func(p *nodeProcess, r nodeReply) {
		if r.Operation == "receive" && delay.Load() {
			queued <- r
			return
		}
		h.adapterRPC(p, r)
	}, h.toolRPC, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.lifecycle.Lock()
	h.process = process
	h.lifecycle.Unlock()
	path, _ := filepath.Abs("testdata/adapter-fixture.mjs")
	raw, err := h.EnableManagedJSON(context.Background(), "adapter-fixture", path, json.RawMessage(`{}`), "initial", func(json.RawMessage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	op, r, err := h.BeginCurrentConfiguration(context.Background(), "adapter-fixture", InspectionRequest{ID: "adapter-fixture"})
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
	delay.Store(true)
	handle := actionHandle(t, raw, "context")
	call := make(chan error, 1)
	go func() {
		_, e := h.Call(context.Background(), "adapter-fixture", handle, []any{"receive"})
		call <- e
	}()
	var event nodeReply
	select {
	case event = <-queued:
	case e := <-call:
		t.Fatal("call returned before captured event", e)
	case <-time.After(5 * time.Second):
		t.Fatal("actual wire event not captured")
	}
	if event.Activation == 0 {
		t.Fatal("wire event omitted immutable generation")
	}
	if err = h.RetireManagedRuntime(context.Background(), "adapter-fixture"); err != nil {
		t.Fatal(err)
	}
	delay.Store(false)
	if _, err = op.Resume(context.Background(), json.RawMessage(`{}`), "replacement", func(json.RawMessage) error { return nil }, nil, nil); err != nil {
		t.Fatal(err)
	}
	before := received.Load()
	if before != 2 {
		t.Fatal("real startup receives missing", before)
	}
	if _, err = h.adapterService(process, event); !errors.Is(err, ErrNotLoaded) {
		t.Fatal("queued old event admitted to replacement", err)
	}
	if received.Load() != before {
		t.Fatal("old event reached native service")
	}
	select {
	case <-call:
	case <-time.After(5 * time.Second):
		t.Fatal("old invocation retained")
	}
	if err = h.Disable(context.Background(), "adapter-fixture"); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryDisableWaitsForPhysicalAdapterCallback(t *testing.T) {
	var block atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	h, err := NewV1HostWithOptions("node", io.Discard, V1HostOptions{AdapterServices: AdapterServices{
		IsRecalled: func(context.Context, AdapterMetadata, string, string) (bool, error) { return false, nil },
		Receive: func(context.Context, AdapterMetadata, dispatch.SceneRef, json.RawMessage) error {
			if block.Load() {
				close(entered)
				<-release
			}
			return nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { releaseOnce.Do(func() { close(release) }); h.Close(context.Background()) }()
	path, _ := filepath.Abs("testdata/adapter-fixture.mjs")
	raw, err := h.EnableManagedJSON(context.Background(), "adapter-fixture", path, json.RawMessage(`{}`), "initial", func(json.RawMessage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	pid := h.PID()
	block.Store(true)
	handle := actionHandle(t, raw, "context")
	call := make(chan error, 1)
	go func() { _, e := h.Call(context.Background(), "adapter-fixture", handle, []any{"receive"}); call <- e }()
	select {
	case <-entered:
	case e := <-call:
		t.Fatal("native callback did not enter", e)
	case <-time.After(5 * time.Second):
		t.Fatal("native callback did not enter")
	}
	disabled := make(chan error, 1)
	go func() { disabled <- h.Disable(context.Background(), "adapter-fixture") }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	exited := false
	for !exited {
		select {
		case e := <-disabled:
			t.Fatal("Disable returned before physical native callback", e)
		case <-timer.C:
			t.Fatal("worker did not exit while callback blocked")
		case <-ticker.C:
			exited = h.PID() == 0 && syscall.Kill(pid, 0) != nil
		}
	}
	select {
	case e := <-disabled:
		t.Fatal("Disable mistook Node exit for callback completion", e)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case e := <-disabled:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("physical callback did not release Disable")
	}
	select {
	case <-call:
	case <-time.After(5 * time.Second):
		t.Fatal("old call did not return")
	}
}
