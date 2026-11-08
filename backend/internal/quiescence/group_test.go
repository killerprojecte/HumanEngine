package quiescence

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
)

func TestSealRejectsRootsButRetainsAcceptedChild(t *testing.T) {
	var g Group
	parent, ok := g.Start()
	if !ok {
		t.Fatal("start")
	}
	g.Seal()
	if _, ok = g.Start(); ok {
		t.Fatal("accepted after seal")
	}
	child, ok := g.Child()
	if !ok {
		t.Fatal("lost admitted parent")
	}
	parent()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(g.Wait(ctx), context.Canceled) {
		t.Fatal("released live child")
	}
	child()
	child()
	if e := g.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, ok = g.Child(); ok {
		t.Fatal("resurrected drained group")
	}
}
func TestRegistrationAndSealRace(t *testing.T) {
	for i := 0; i < 1000; i++ {
		var g Group
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			if done, ok := g.Start(); ok {
				done()
			}
		}()
		if e := g.Wait(context.Background()); e != nil {
			t.Fatal(e)
		}
		wg.Wait()
		if _, ok := g.Start(); ok {
			t.Fatal("late start")
		}
	}
}

func TestUncertainStarterReportsWithoutReleasingPhysicalWork(t *testing.T) {
	var g Group
	done, _ := g.Start()
	expected := errors.New("synthetic_unsettled_starter")
	resolve := g.MarkUncertain(expected)
	if e := g.Wait(context.Background()); !errors.Is(e, expected) {
		t.Fatal("unknown callback outcome not reported", e)
	}
	resolve()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := g.Wait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal("reporting uncertainty released physical owner", e)
	}
	done()
	if e := g.Wait(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestUncertaintyWakesAlreadyWaitingBackgroundDrain(t *testing.T) {
	var g Group
	physicalDone, _ := g.Start()
	returned := make(chan error, 1)
	go func() { returned <- g.Wait(context.Background()) }()
	// Wait has sealed admission and installed its notification channel before we
	// report uncertainty. No sleeps or production test hooks are needed.
	for {
		g.mu.Lock()
		waiting := g.sealed && g.changed != nil
		g.mu.Unlock()
		if waiting {
			break
		}
		runtime.Gosched()
	}
	select {
	case e := <-returned:
		t.Fatalf("physical owner was not retained: %v", e)
	default:
	}
	expected := errors.New("synthetic_starter_failed_pending_settlement")
	resolve := g.MarkUncertain(expected)
	if e := <-returned; !errors.Is(e, expected) {
		t.Fatalf("blocked Background wait not notified: %v", e)
	}
	resolve()
	physicalDone()
	if e := g.Wait(context.Background()); e != nil {
		t.Fatalf("actual completion did not permit retry: %v", e)
	}
}
