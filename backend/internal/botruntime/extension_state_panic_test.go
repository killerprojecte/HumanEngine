package botruntime

import (
	"testing"
	"time"
)

func TestTurnOwnedStatePanicsDoNotPoisonMutex(t *testing.T) {
	for _, kind := range []string{"allocator", "unhashable-key"} {
		t.Run(kind, func(t *testing.T) {
			c := &TurnContext{}
			var cause any
			func() {
				defer func() { cause = recover() }()
				if kind == "allocator" {
					c.OwnedExtensionState("broken", func() any { panic("synthetic allocation panic") })
				} else {
					c.OwnedExtensionState([]string{"not-comparable"}, func() any { return nil })
				}
			}()
			if cause == nil {
				t.Fatal("original panic was not preserved")
			}
			done := make(chan error, 1)
			resource := &testTurnResource{release: func() error { c.HasSourceSnapshots(); return nil }}
			go func() {
				c.HasSourceSnapshots()
				c.OwnedExtensionState("valid", func() any { return resource })
				done <- c.releaseExtensionState()
			}()
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("panic kept turn mutex locked")
			}
			if resource.calls.Load() != 1 {
				t.Fatal(resource.calls.Load())
			}
		})
	}
}
