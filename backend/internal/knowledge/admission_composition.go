package knowledge

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// ReadAssertionCurrent borrows only the explicitly active assertion child.
// It cannot open a new publication or transaction and retains reader expiry.
func (s *Store) ReadAssertionCurrent(ctx context.Context, assertionStore *assertions.Store, commitmentStore *commitments.Store, options ReadOptions, fn func(*Reader) error) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if fn == nil || assertionStore == nil || commitmentStore == nil {
		return errors.New("knowledge_current_owner_required")
	}
	if e := assertionStore.CheckReadBinding(s.authority, s.main, s.session); e != nil {
		return e
	}
	if options.Authority.DateParser == nil || options.Authority.NowMillis == nil {
		return errors.New("knowledge_time_policy_required")
	}
	return commitmentStore.ReadActiveAssertionCurrent(ctx, s.session, assertionStore.WithSession(s.session), func(commitments.Context) (bool, error) { return options.CommitmentEnabled, nil }, options.Authority, func(current *commitments.ReadService) error {
		check := func() error {
			if e := s.RequireTransaction(); e != nil {
				return e
			}
			return current.AssertReady()
		}
		r := &Reader{storage: s, ready: check, dates: options.Authority.DateParser, now: options.Authority.NowMillis, enabled: func(Context) (bool, error) {
			if e := check(); e != nil {
				return false, e
			}
			return options.KnowledgeEnabled, nil
		}, current: func(c Context, ref Reference) (*Assertion, error) {
			if e := check(); e != nil {
				return nil, e
			}
			return current.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, ref)
		}}
		return errors.Join(fn(r), check())
	})
}

// BindAdmissionSession permits an unbound root or this exact active child only.
func (s *Store) BindAdmissionSession(session *recovery.WriteSession) (*Store, error) {
	if s.bindingError != nil {
		return nil, s.bindingError
	}
	if s.session != nil && s.session != session {
		return nil, errors.New("knowledge_store_session_binding")
	}
	bound := s.WithSession(session)
	if e := bound.RequireTransaction(); e != nil {
		return nil, e
	}
	return bound, nil
}
