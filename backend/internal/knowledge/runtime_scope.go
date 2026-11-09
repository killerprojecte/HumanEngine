package knowledge

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"sync/atomic"
)

// ReadRuntimeCurrent borrows a genuine model read or current writer scope. The
// unbound domain owner never reacquires publication or stores a current session.
// Readers and their storage capabilities expire with this exact callback.
func (s *Store) ReadRuntimeCurrent(scope sourceevents.RuntimeReadScope, assertionStore *assertions.Store, commitmentStore *commitments.Store, options ReadOptions, fn func(*Reader) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil || s.maintenance != nil {
		return errors.New("knowledge_unbound_runtime_owner_required")
	}
	if assertionStore == nil || commitmentStore == nil || fn == nil {
		return errors.New("knowledge_current_owner_required")
	}
	if err := scope.CheckBinding(s.main, s.authority); err != nil {
		return err
	}
	if options.Authority.DateParser == nil || options.Authority.NowMillis == nil {
		return errors.New("knowledge_time_policy_required")
	}
	return commitmentStore.ReadRuntimeCurrent(scope, assertionStore, func(commitments.Context) (bool, error) { return options.CommitmentEnabled, nil }, options.Authority, func(current *commitments.ReadService) error {
		var active atomic.Bool
		active.Store(true)
		defer active.Store(false)
		check := func() error {
			if !active.Load() {
				return sql.ErrTxDone
			}
			if err := scope.CheckBinding(s.main, s.authority); err != nil {
				return err
			}
			return current.AssertReady()
		}
		storage := &scopeStorage{query: func(q string, args ...any) (workingset.Rows, error) { return scope.Query(q, args...) }, check: check}
		reader := &Reader{storage: storage, ready: check, dates: options.Authority.DateParser, now: options.Authority.NowMillis,
			enabled: func(Context) (bool, error) {
				if err := check(); err != nil {
					return false, err
				}
				return options.KnowledgeEnabled, nil
			},
			current: func(c Context, ref Reference) (*Assertion, error) {
				if err := check(); err != nil {
					return nil, err
				}
				return current.CurrentAssertion(commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation}, ref)
			},
		}
		return errors.Join(fn(reader), check())
	})
}
