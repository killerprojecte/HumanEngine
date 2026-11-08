package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// ReadMaintenanceEpisode uses real current authority and knowledge rows inside
// the exact guarded offline transaction. The reader expires with its callback.
func (s *Store) ReadMaintenanceEpisode(ctx context.Context, assertionStore *assertions.Store, commitmentStore *commitments.Store, options ReadOptions, fn func(*Reader, *commitments.ReadService) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if fn == nil {
		return errors.New("knowledge_read_operation_required")
	}
	if s.maintenance == nil {
		return errors.New("knowledge_maintenance_owner_required")
	}
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if assertionStore == nil || commitmentStore == nil {
		return errors.New("knowledge_current_owner_required")
	}
	if options.Authority.DateParser == nil || options.Authority.NowMillis == nil {
		return errors.New("knowledge_time_policy_required")
	}
	return assertionStore.WithMaintenanceScope(func(tx *recovery.MaintenanceTx) error {
		if tx != s.maintenanceTx {
			return errors.New("knowledge_store_session_binding")
		}
		return commitmentStore.ReadMaintenanceCurrent(ctx, assertionStore, func(commitments.Context) (bool, error) { return options.CommitmentEnabled, nil }, options.Authority, func(current *commitments.ReadService) error {
			var active atomic.Bool
			active.Store(true)
			defer active.Store(false)
			check := func() error {
				if !active.Load() {
					return sql.ErrTxDone
				}
				if e := s.RequireTransaction(); e != nil {
					return e
				}
				return current.AssertReady()
			}
			storage := &maintenanceReadStorage{store: s, check: check}
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
	})
}

// Keep read callbacks guarded even when a Reader is retained after the episode.
type maintenanceReadStorage struct {
	store *Store
	check func() error
}

func (s *maintenanceReadStorage) Rows(scene *string, limit *int) ([]*Object, error) {
	if e := s.check(); e != nil {
		return nil, e
	}
	return s.store.Rows(scene, limit)
}
func (s *maintenanceReadStorage) Get(id string) (*Object, error) {
	if e := s.check(); e != nil {
		return nil, e
	}
	return s.store.Get(id)
}
func (s *maintenanceReadStorage) Entity(id, scene string) (*Object, error) {
	if e := s.check(); e != nil {
		return nil, e
	}
	return s.store.Entity(id, scene)
}
