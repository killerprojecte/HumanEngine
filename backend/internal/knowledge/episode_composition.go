package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

// ReadWorkingSetEpisode composes real current assertion authority and knowledge
// SQL within one trusted concrete working-set owner. The callback expires before
// the owner exits, and cannot acquire a second publication or connection.
func (s *Store) ReadWorkingSetEpisode(ctx context.Context, scope *workingset.Scope, assertionStore *assertions.Store, commitmentStore *commitments.Store, options ReadOptions, fn func(*Reader, *commitments.ReadService) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if fn == nil {
		return errors.New("knowledge_read_operation_required")
	}
	if e := scope.CheckReadBinding(s.authority, s.main); e != nil {
		return e
	}
	if s.session != nil && s.session != scope.WriteSession() {
		return errors.New("knowledge_store_session_binding")
	}
	if assertionStore == nil || commitmentStore == nil {
		return errors.New("knowledge_current_owner_required")
	}
	if options.Authority.DateParser == nil || options.Authority.NowMillis == nil {
		return errors.New("knowledge_time_policy_required")
	}
	return commitmentStore.ReadWorkingSetCurrent(ctx, scope, assertionStore, func(commitments.Context) (bool, error) { return options.CommitmentEnabled, nil }, options.Authority, func(current *commitments.ReadService) error {
		var active atomic.Bool
		active.Store(true)
		defer active.Store(false)
		check := func() error {
			if !active.Load() {
				return sql.ErrTxDone
			}
			if e := scope.CheckReadBinding(s.authority, s.main); e != nil {
				return e
			}
			return current.AssertReady()
		}
		storage := &scopeStorage{query: scope.Query, check: check}
		r := &Reader{storage: storage, ready: check, dates: options.Authority.DateParser, now: options.Authority.NowMillis,
			enabled: func(Context) (bool, error) {
				if e := check(); e != nil {
					return false, e
				}
				return options.KnowledgeEnabled, nil
			},
			current: func(c Context, ref Reference) (*Assertion, error) {
				if e := check(); e != nil {
					return nil, e
				}
				return current.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, ref)
			},
		}
		return errors.Join(fn(r, current), check())
	})
}
