package botruntime

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

type testTurnResource struct {
	calls   atomic.Int32
	release func() error
}

func (r *testTurnResource) ReleaseTurnState() error {
	r.calls.Add(1)
	if r.release != nil {
		return r.release()
	}
	return nil
}
func TestTurnOwnedStateCleanupAndLateAllocation(t *testing.T) {
	c := &TurnContext{}
	resource := &testTurnResource{release: func() error { c.HasSourceSnapshots(); return errors.New("release_failed") }}
	if got := c.OwnedExtensionState("resource", func() any { return resource }); got != resource {
		t.Fatal("identity")
	}
	if err := c.releaseExtensionState(); err == nil || err.Error() != "release_failed" {
		t.Fatal(err)
	}
	if err := c.releaseExtensionState(); err != nil {
		t.Fatal(err)
	}
	if resource.calls.Load() != 1 {
		t.Fatal(resource.calls.Load())
	}
	late := &testTurnResource{}
	c.OwnedExtensionState("late", func() any { return late })
	if late.calls.Load() != 1 {
		t.Fatal("late state leaked")
	}
}
func TestTurnOwnedStateAllocationCleanupRace(t *testing.T) {
	for i := 0; i < 100; i++ {
		c := &TurnContext{}
		resource := &testTurnResource{}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); c.OwnedExtensionState("resource", func() any { return resource }) }()
		go func() { defer wg.Done(); _ = c.releaseExtensionState() }()
		wg.Wait()
		if resource.calls.Load() != 1 {
			t.Fatal(resource.calls.Load())
		}
	}
}
