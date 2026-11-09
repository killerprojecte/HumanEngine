package logdelivery

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLeasePhysicalReleaseBelongsToFirstRelease(t *testing.T) {
	gate := make(chan struct{})
	physical := make(chan struct{})
	r, count := memoryRegistry(t, gate, &memorySink{closeGate: physical})
	a := lease(t, r, "/logs")
	b := lease(t, r, "/logs")
	if !errors.Is(a.WaitPhysicalRelease(context.Background()), ErrLeaseNotReleased) {
		t.Fatal("unreleased lease accepted")
	}
	a.Enqueue("one\n", false)
	closing := beginClose(t, a, DefaultTimeout)
	close(gate)
	answer(t, closing)
	if e := a.WaitPhysicalRelease(context.Background()); e != nil {
		t.Fatal(e)
	}
	// B is last. Its native cleanup is held even though A remains readable.
	stopped, e := b.CloseWithTimeout(context.Background(), time.Millisecond)
	if e != nil || stopped.Complete {
		t.Fatal(stopped, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(b.WaitPhysicalRelease(ctx), context.Canceled) {
		t.Fatal("last release ignored held physical tail")
	}
	if e := a.WaitPhysicalRelease(context.Background()); e != nil {
		t.Fatal("nonlast was retroactively made responsible", e)
	}
	next := lease(t, r, "/logs")
	if next.Enqueue("blocked\n", false) || *count != 1 {
		t.Fatal("overlapping canonical owner")
	}
	before := *count
	_ = a.Status()
	_ = b.Status()
	_ = next.Status()
	if *count != before {
		t.Fatal("status replaced a generation")
	}
	close(physical)
	if e := b.WaitPhysicalRelease(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := b.WaitPhysicalRelease(ctx); e != nil {
		t.Fatal("physical completion lost cancellation race", e)
	}
	if !next.Enqueue("new\n", false) || *count != 2 {
		t.Fatal("replacement not available")
	}
	if e := b.WaitPhysicalRelease(context.Background()); e != nil {
		t.Fatal("retired wait switched generation", e)
	}
	closeLease(t, next)
	if e := next.WaitPhysicalRelease(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestBeginCloseAdmitsWithoutWaitingAndDetachedWaitRetainsTail(t *testing.T) {
	gate := make(chan struct{})
	r, _ := memoryRegistry(t, gate, nil)
	a := lease(t, r, "/logs")
	a.Enqueue("one\n", false)
	operation := a.BeginClose(DefaultTimeout)
	if a.owner.channel.Status().State != "closed" {
		t.Fatal("close was not initiated synchronously")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := operation.Wait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if a.Enqueue("after\n", false) {
		t.Fatal("released admission accepted")
	}
	close(gate)
	result, e := operation.Wait(context.Background())
	if e != nil || result.Status.WrittenRecords != 1 || result.Complete {
		t.Fatal(result, e)
	}
	if e = a.WaitPhysicalRelease(context.Background()); e != nil {
		t.Fatal(e)
	}
}
