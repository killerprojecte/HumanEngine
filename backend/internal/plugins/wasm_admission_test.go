package plugins

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWASMAdmissionCancellationDoesNotInterruptSynchronousExecution(t *testing.T) {
	ctx := context.Background()
	h, e := NewWASMHost(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close(ctx)
	entered, release := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	if _, e = h.runtime.NewHostModuleBuilder("probe").NewFunctionBuilder().WithFunc(func(ctx context.Context) {
		if probes.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}).Export("entered").Instantiate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = h.Enable(ctx, "admission", lifecycleWASM([]byte{0x10, 0, 0x0b}, nil)); e != nil {
		t.Fatal(e)
	}
	call, e := h.BindCallWithAdmission("admission")
	if e != nil {
		t.Fatal(e)
	}
	wait, cancel := context.WithCancel(ctx)
	first := make(chan error, 1)
	go func() {
		out, e := call(wait, ctx, []byte(`{}`))
		if e == nil && string(out) != "{}" {
			e = errors.New("incorrect synchronous result")
		}
		first <- e
	}()
	<-entered
	cancel()
	select {
	case e := <-first:
		t.Fatal("wait cancellation interrupted admitted execution", e)
	default:
	}
	queued, cancelQueued := context.WithCancel(ctx)
	second := make(chan error, 1)
	go func() { _, e := call(queued, ctx, []byte(`{}`)); second <- e }()
	cancelQueued()
	if e = waitWASMResult(t, second); !errors.Is(e, context.Canceled) {
		t.Fatal("queued caller not cancelled", e)
	}
	if !h.Loaded("admission") || probes.Load() != 1 {
		t.Fatal("queue cancellation touched executing generation", probes.Load())
	}
	close(release)
	if e = waitWASMResult(t, first); e != nil {
		t.Fatal("admitted call failed after waiter cancellation", e)
	}
	if _, e = call(ctx, ctx, []byte(`{}`)); e != nil {
		t.Fatal("generation unusable", e)
	}
}
func TestWASMAdmissionUnloadJoinsExecutionAndRejectsReplacement(t *testing.T) {
	ctx := context.Background()
	h, e := NewWASMHost(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close(ctx)
	entered, returned := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	if _, e = h.runtime.NewHostModuleBuilder("probe").NewFunctionBuilder().WithFunc(func(ctx context.Context) {
		if probes.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			close(returned)
		}
	}).Export("entered").Instantiate(ctx); e != nil {
		t.Fatal(e)
	}
	artifact := lifecycleWASM([]byte{0x10, 0, 0x0b}, nil)
	if e = h.Enable(ctx, "admission", artifact); e != nil {
		t.Fatal(e)
	}
	old, e := h.BindCallWithAdmission("admission")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := old(ctx, ctx, []byte(`{}`)); done <- e }()
	<-entered
	if e = h.Disable(ctx, "admission"); e != nil {
		t.Fatal(e)
	}
	if e = waitWASMResult(t, done); e == nil {
		t.Fatal("unloaded call reported success")
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("source callback failed to finish")
	}
	if e = h.Enable(ctx, "admission", artifact); e != nil {
		t.Fatal(e)
	}
	if _, e = old(ctx, ctx, []byte(`{}`)); !errors.Is(e, ErrNotLoaded) {
		t.Fatal("old capability entered replacement", e)
	}
	next, e := h.BindCallWithAdmission("admission")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = next(ctx, ctx, []byte(`{}`)); e != nil {
		t.Fatal(e)
	}
}
