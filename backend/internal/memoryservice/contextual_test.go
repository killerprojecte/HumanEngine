package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestContextualGuardedProposalRealWriterAndRollback(t *testing.T) {
	s, f, options := outerSetup(t)
	ctx := context.Background()
	input, candidate := outerInput(t, "guarded")
	_ = input.Set("provenance", "contextual")
	_ = input.Set("dependencyMemoryIds", []any{})
	_ = input.Set("dependencyParentReferences", []any{})
	rejected := errors.New("context_ineligible")
	_, e := s.ProposeGuarded(ctx, func(ws *recovery.WriteSession) (*Object, *Object, ProposalOptions, error) {
		if ws == nil {
			t.Fatal("no actual writer")
		}
		states, e := s.ContextualStates(ctx, "s", []string{"missing"}, ws)
		if e != nil {
			return nil, nil, ProposalOptions{}, e
		}
		if states["missing"].Visible {
			t.Fatal(states)
		}
		return input, candidate, options, rejected
	})
	if !errors.Is(e, rejected) {
		t.Fatal(e)
	}
	if n := outerCount(t, f, "SELECT count(*) FROM memories"); n != 0 {
		t.Fatal("rejected guard wrote", n)
	}
	r, e := s.ProposeGuarded(ctx, func(ws *recovery.WriteSession) (*Object, *Object, ProposalOptions, error) {
		if ws == nil {
			t.Fatal("writer missing")
		}
		return input, candidate, options, nil
	})
	requireOuterSaved(t, r, e)
	ref := outerRef(t, f, r)
	if text(ref, "id") == "" {
		t.Fatal(ref)
	}
	states, e := s.ContextualStates(ctx, "s", []string{text(r.Memory, "id")}, nil)
	if e != nil {
		t.Fatal(e)
	}
	state := states[text(r.Memory, "id")]
	if !state.Visible || state.Deleted || state.Version != 1 {
		t.Fatal(states)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	called := false
	_, e = s.ProposeGuarded(cancelled, func(*recovery.WriteSession) (*Object, *Object, ProposalOptions, error) {
		called = true
		return input, candidate, options, nil
	})
	if e == nil || called {
		t.Fatal("cancelled write admitted", e, called)
	}
}
