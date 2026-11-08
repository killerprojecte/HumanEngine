package botruntime

import (
	"context"
	"testing"
)

func TestClearDeferredTopicsPreservesAcceptedRouting(t *testing.T) {
	r, _, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {}))
	defer r.Close()
	registerFixture(t, r)
	s := r.state("scene")
	s.mu.Lock()
	s.routing.Defer("deferred", true)
	s.routing.Overflowed = 7
	s.routing.Acknowledge("ack", "topic")
	s.routing.ProtectedSends.Set("protected", struct{}{})
	s.routing.Turns.Set("active", "topic")
	pending := s.queue.Len()
	s.mu.Unlock()
	if err := r.ClearDeferredTopics(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if s.routing.Deferred.Len() != 0 || s.routing.Overflowed != 7 || s.routing.Acknowledged.Len() != 1 || s.routing.ProtectedSends.Len() != 1 || s.routing.Turns.Len() != 1 || s.queue.Len() != pending {
		t.Fatal("settings change altered accepted routing")
	}
	s.mu.Unlock()
	r.Close()
	if err := r.ClearDeferredTopics(); err == nil {
		t.Fatal("closed runtime accepted settings effect")
	}
	if err := r.ConfigureAdmission(DefaultSettings()); err == nil {
		t.Fatal("closed runtime accepted admission change")
	}
}
