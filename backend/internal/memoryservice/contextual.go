package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// ProposeGuarded prepares trusted proposal input and revalidates all exposure,
// source and signal authority inside the actual writer, before any proposal
// mutation. Nested proposal savepoints keep normal revision and typed-domain
// hooks. Post-commit projection flush runs once, after the enclosing root commits.
func (s *Service) ProposeGuarded(ctx context.Context, prepare func(*recovery.WriteSession) (*Object, *Object, ProposalOptions, error)) (out ProposalResult, err error) {
	if prepare == nil {
		return out, errors.New("contextual_proposal_guard_required")
	}
	err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			input, candidate, options, e := prepare(ws)
			if e != nil {
				return e
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			copy := *s
			copy.d.Assertions = s.d.Assertions.WithSession(ws)
			copy.revisions = s.revisions.WithSession(ws)
			out, e = copy.Propose(ctx, input, candidate, options)
			return e
		})
	})
	return
}

type ContextualState struct {
	Version, DeletionEpoch int64
	Deleted, Visible       bool
}

// ContextualStates preserves MemoryService.list's support refresh and visible
// set, plus exact revisionState, on a supplied writer when checking a commit.
func (s *Service) ContextualStates(ctx context.Context, scene string, ids []string, ws *recovery.WriteSession) (map[string]ContextualState, error) {
	copy := *s
	if ws != nil {
		copy.d.Assertions = s.d.Assertions.WithSession(ws)
		copy.revisions = s.revisions.WithSession(ws)
	}
	visible, e := copy.List(ctx, Context{SceneID: scene}, "")
	if e != nil {
		return nil, e
	}
	set := map[string]bool{}
	for _, m := range visible {
		set[text(m, "id")] = true
	}
	out := map[string]ContextualState{}
	e = copy.d.Assertions.Read(ctx, func(r *assertions.Reader) error {
		for _, id := range ids {
			if _, ok := out[id]; ok {
				continue
			}
			state, e := r.State(id)
			if e != nil {
				return e
			}
			out[id] = ContextualState{state.Version, state.DeletionEpoch, state.Deleted, set[id]}
		}
		return nil
	})
	return out, e
}
