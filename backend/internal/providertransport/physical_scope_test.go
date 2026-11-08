package providertransport

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"sync"
	"testing"
	"time"
)

func awaitPhysical(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(3 * time.Second):
		t.Fatal("physical callback did not enter")
	}
}
func TestBorrowedLearningScopeJoinsCancelledPhysicalCallback(t *testing.T) {
	m := new(NativeModels)
	var scope quiescence.Group
	root, _ := scope.Start()
	parent, cancel := context.WithCancel(context.Background())
	ctx := WithPhysicalWork(parent, &scope)
	entered, release, callback, releaseCallback, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, callbackOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		callbackOnce.Do(func() { close(releaseCallback) })
		cancel()
		wait, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		if e := m.CloseAndWait(wait); e != nil {
			t.Error(e)
		}
	})
	invoke := m.asyncInvocation(func(_ context.Context, _ modelservice.Attempt, settle func(any, error)) {
		close(entered)
		<-release
		settle(nil, nil)
	})
	invoke(ctx, modelservice.Attempt{}, func(any, error) { close(callback); <-releaseCallback; close(finished) })
	awaitPhysical(t, entered)
	cancel()
	root()
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if e := scope.Wait(cancelled); !errors.Is(e, context.Canceled) {
		t.Fatalf("released cancelled physical provider: %v", e)
	}
	releaseOnce.Do(func() { close(release) })
	awaitPhysical(t, callback)
	if e := scope.Wait(cancelled); !errors.Is(e, context.Canceled) {
		t.Fatalf("released late callback: %v", e)
	}
	callbackOnce.Do(func() { close(releaseCallback) })
	awaitPhysical(t, finished)
	if e := scope.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
	// Draining one learning batch cannot close the shared provider used by a turn.
	next := make(chan struct{})
	m.asyncInvocation(func(_ context.Context, _ modelservice.Attempt, done func(any, error)) { done(nil, nil) })(context.Background(), modelservice.Attempt{}, func(_ any, e error) {
		if e != nil {
			t.Error(e)
		}
		close(next)
	})
	awaitPhysical(t, next)
	// A late scheduler starter cannot resurrect an already drained batch scope.
	late := make(chan error, 1)
	called := false
	m.asyncInvocation(func(_ context.Context, _ modelservice.Attempt, done func(any, error)) { called = true; done(nil, nil) })(ctx, modelservice.Attempt{}, func(_ any, e error) { late <- e })
	select {
	case e := <-late:
		if e == nil || called {
			t.Fatal("late scoped physical work revived")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late rejection missing")
	}
}
