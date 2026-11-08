package memeservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sync"
	"testing"
)

func TestAsyncDeleteOwnedOperationRejectsNestedAndMissing(t *testing.T) {
	f, s := newTestService(t)
	ctx := context.Background()
	id := identity("synthetic")
	if _, e := s.AdminDelete(ctx, id); e == nil || e.Error() != "recovery_operation_context_required" {
		t.Fatalf("missing ownership: %v", e)
	}
	op, release, e := f.authority.OperationContext(ctx, f.main)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if e = f.authority.WithWriteSession(op, f.main, func(ws *recovery.WriteSession) error {
		// This is a genuine synchronous call while the only pooled connection is
		// owned. No timeout is supplied to turn the bug into a successful return.
		if _, e := s.AdminDelete(op, id); e == nil || e.Error() != "recovery_async_transaction_not_allowed" {
			return errors.New("nested async delete not rejected before pool acquisition")
		}
		if _, _, e := f.authority.OperationContext(op, f.main); e == nil || e.Error() != "recovery_async_transaction_not_allowed" {
			return errors.New("nested operation silently replaced owner")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	head, e := f.authority.Head(ctx)
	if e != nil || head.Sequence != 0 {
		t.Fatal("rejection appended intent", head, e)
	}
	f2 := newMemeFixture(t)
	if e = f.authority.RequireAsyncRoot(op, f2.main); e == nil || e.Error() != "recovery_operation_binding" {
		t.Fatal("foreign main accepted", e)
	}
	if e = f2.authority.RequireAsyncRoot(op, f.main); e == nil || e.Error() != "recovery_operation_binding" {
		t.Fatal("foreign authority accepted", e)
	}
	inherited, innerRelease, e := f.authority.OperationContext(op, f.main)
	if e != nil {
		t.Fatal(e)
	}
	innerRelease()
	if e = f.authority.RequireAsyncRoot(inherited, f.main); e != nil {
		t.Fatal("borrowed release ended root", e)
	}
	release()
	if e = f.authority.RequireAsyncRoot(op, f.main); e == nil || e.Error() != "recovery_operation_expired" {
		t.Fatal("expired operation accepted", e)
	}
}
func TestIndependentRootOperationWaitsForWriterWithoutFalseRejection(t *testing.T) {
	f, _ := newTestService(t)
	root := context.Background()
	first, endFirst, e := f.authority.OperationContext(root, f.main)
	if e != nil {
		t.Fatal(e)
	}
	defer endFirst()
	second, endSecond, e := f.authority.OperationContext(root, f.main)
	if e != nil {
		t.Fatal(e)
	}
	defer endSecond()
	entered := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	defer release()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- f.authority.WithWriteSession(first, f.main, func(ws *recovery.WriteSession) error { close(entered); <-resume; return nil })
	}()
	<-entered
	before := f.main.Stats().WaitCount
	type outcome struct {
		ticket *recovery.DurableTicket
		err    error
	}
	secondDone := make(chan outcome, 1)
	go func() {
		ticket, e := f.authority.BeginDurable(second, f.main, func(ws *recovery.WriteSession) (recovery.Revocation, error) {
			return recovery.Source{SceneID: "s", MessageID: "m"}, nil
		})
		secondDone <- outcome{ticket, e}
	}()
	waitUntil(t, func() bool { return f.main.Stats().WaitCount > before })
	select {
	case result := <-secondDone:
		t.Fatalf("independent operation rejected rather than queued: %v", result.err)
	default:
	}
	// Same capability sharing is an ownership error, not a global database cap.
	if e = f.authority.RequireAsyncRoot(first, f.main); e == nil || e.Error() != "recovery_async_transaction_not_allowed" {
		t.Fatal(e)
	}
	release()
	if e = <-firstDone; e != nil {
		t.Fatal(e)
	}
	result := <-secondDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	if e = f.authority.CommitDurable(second, f.main, result.ticket, func(ws *recovery.WriteSession) error { return nil }); e != nil {
		t.Fatal(e)
	}
}

func TestForkTaskOperationBindingLifetimeAndOwnership(t *testing.T) {
	f, _ := newTestService(t)
	root, releaseRoot, err := f.authority.OperationContext(context.Background(), f.main)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRoot()
	assertError := func(ctx context.Context, want string) {
		t.Helper()
		_, release, e := f.authority.ForkTaskOperation(ctx, f.main)
		if release != nil {
			release()
		}
		if e == nil || e.Error() != want {
			t.Fatalf("fork: got %v, want %s", e, want)
		}
	}
	assertError(context.Background(), "recovery_operation_context_required")
	foreign := newMemeFixture(t)
	foreignRoot, endForeign, e := foreign.authority.OperationContext(context.Background(), foreign.main)
	if e != nil {
		t.Fatal(e)
	}
	defer endForeign()
	assertError(foreignRoot, "recovery_operation_binding")
	if _, release, e := f.authority.ForkTaskOperation(root, foreign.main); e == nil || e.Error() != "recovery_operation_binding" {
		if release != nil {
			release()
		}
		t.Fatal("foreign main", e)
	}
	if _, release, e := foreign.authority.ForkTaskOperation(root, f.main); e == nil || e.Error() != "recovery_operation_binding" {
		if release != nil {
			release()
		}
		t.Fatal("foreign authority", e)
	}
	if e := f.authority.WithWriteSession(root, f.main, func(_ *recovery.WriteSession) error {
		assertError(root, "recovery_async_transaction_not_allowed")
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	type valueKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(root, valueKey{}, "preserved"))
	task, endTask, e := f.authority.ForkTaskOperation(parent, f.main)
	if e != nil {
		t.Fatal(e)
	}
	defer endTask()
	cancel()
	releaseRoot()
	assertError(root, "recovery_operation_expired")
	if task.Err() != nil || task.Value(valueKey{}) != "preserved" {
		t.Fatal("task did not preserve values and independent cancellation")
	}
	if e = f.authority.WithWriteSession(task, f.main, func(_ *recovery.WriteSession) error { return nil }); e != nil {
		t.Fatal(e)
	}
	endTask()
	endTask()
	assertError(task, "recovery_operation_expired")
	if e = f.authority.WithWriteSession(task, f.main, func(_ *recovery.WriteSession) error { t.Fatal("expired task entered writer"); return nil }); e == nil || e.Error() != "recovery_operation_expired" {
		t.Fatal(e)
	}
}
