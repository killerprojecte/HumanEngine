package modelservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
	"sync"
	"testing"
	"time"
)

func TestBorrowedScopeRetainsRoleTimeoutRunnerAndMetadataCallback(t *testing.T) {
	clock := &fakeClock{}
	p, e := New(baseConfig("openai-chat", 100), Options{Clock: clock, Scheduler: scheduler.NewModelScheduler()})
	if e != nil {
		t.Fatal(e)
	}
	var group quiescence.Group
	root, _ := group.Start()
	started := make(chan func(any, error), 1)
	returned := make(chan error, 1)
	observerEntered, releaseObserver := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() { close(releaseObserver) })
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if e := p.Drain(ctx); e != nil {
			t.Error(e)
		}
	})
	ctx := WithAttemptObserver(quiescence.WithGroup(context.Background(), &group), func(context.Context, AttemptObservation) { close(observerEntered); <-releaseObserver })
	go func() {
		_, e := p.Run(ctx, Request{Role: "replyer"}, func(_ context.Context, _ Attempt, done func(any, error)) { started <- done })
		returned <- e
	}()
	var settle func(any, error)
	select {
	case settle = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider not entered")
	}
	clock.advance(100)
	select {
	case e := <-returned:
		if e == nil {
			t.Fatal("role did not timeout")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("role timeout did not return")
	}
	root()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if e := group.Wait(cancelled); !errors.Is(e, context.Canceled) {
		t.Fatalf("role timeout released policy runner: %v", e)
	}
	settle(nil, errors.New("synthetic_settlement"))
	select {
	case <-observerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("metadata callback not entered")
	}
	if e := group.Wait(cancelled); !errors.Is(e, context.Canceled) {
		t.Fatalf("released actual metadata SQL callback: %v", e)
	}
	once.Do(func() { close(releaseObserver) })
	wait, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if e := group.Wait(wait); e != nil {
		t.Fatal(e)
	}
	// Caller-scope draining must leave this borrowed policy available.
	_, e = p.Run(context.Background(), Request{Role: "replyer"}, func(_ context.Context, _ Attempt, done func(any, error)) { done("next", nil) })
	if e != nil {
		t.Fatal("shared policy was closed", e)
	}
}
