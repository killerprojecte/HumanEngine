package plugins

import (
	"context"
	"errors"
	"testing"
)

// Count actual host protocol frames, not a substitute getter or cached status.
// Combined with Runtime's counted source oracle, this proves two recognized
// property reads produce two RPCs, never four hidden normalization reads.
func TestAdapterStatusOneRPCPerRawRead(t *testing.T) {
	fixture := newAdapterFixture(t, map[string]any{})
	sequence := func() uint64 {
		fixture.host.lifecycle.Lock()
		process := fixture.host.process
		fixture.host.lifecycle.Unlock()
		if process == nil {
			t.Fatal("actual V1 process missing")
		}
		process.mu.Lock()
		defer process.mu.Unlock()
		return process.next
	}
	for i := 0; i < 2; i++ {
		before := sequence()
		status, err := fixture.host.AdapterStatus(context.Background(), fixture.meta.ID, fixture.meta.Generation)
		after := sequence()
		if err != nil || status != "connected" || after-before != 1 {
			t.Fatalf("status=%q error=%v RPCs=%d", status, err, after-before)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := sequence()
	if _, err := fixture.host.AdapterStatus(ctx, fixture.meta.ID, fixture.meta.Generation); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if sequence() != before {
		t.Fatal("cancelled read invoked worker")
	}
}
