package botruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSentSequencerCloseWaitRetainsRunningCallback(t *testing.T) {
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	sequence := NewSentSequencer(owner, func(Scene, string, string) { close(entered); <-release; close(returned) })
	reservation := sequence.reserve(Scene{}, "turn", "message")
	reservation.Release()
	<-entered
	cancelOwner()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := sequence.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal("running callback reported drained", e)
	}
	if sequence.reserve(Scene{}, "late", "late") != nil {
		t.Fatal("reservation entered after close")
	}
	select {
	case <-returned:
		t.Fatal("fixture released early")
	default:
	}
	unblock()
	if e := sequence.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	select {
	case <-returned:
	default:
		t.Fatal("callback not physically returned")
	}
}

func TestSentSequencerConcurrentReserveAndClose(t *testing.T) {
	for round := 0; round < 16; round++ {
		var entered, accepted atomic.Int64
		sequence := NewSentSequencer(context.Background(), func(Scene, string, string) { entered.Add(1) })
		start := make(chan struct{})
		var workers sync.WaitGroup
		for i := 0; i < 16; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				r := sequence.reserve(Scene{}, "t", "m")
				if r != nil {
					accepted.Add(1)
					r.Release()
				}
			}()
		}
		done := make(chan error, 1)
		go func() { <-start; done <- sequence.CloseAndWait(context.Background()) }()
		close(start)
		workers.Wait()
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		if entered.Load() != accepted.Load() {
			t.Fatalf("unjoined accepted reservation callbacks=%d accepted=%d", entered.Load(), accepted.Load())
		}
		if sequence.reserve(Scene{}, "late", "late") != nil {
			t.Fatal("unsealed close")
		}
	}
}
