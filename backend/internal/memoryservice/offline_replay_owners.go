package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryworkspace"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"time"
)

func (o *OfflineLifecycle) CommitmentRecovery(ctx context.Context) (*commitments.OfflineMaintenance, error) {
	return commitments.NewOfflineMaintenance(o.owner, o.options.Commitments, commitments.Owners{AfterWrite: func(store *commitments.Store, head *Object, next, prior []any) error {
		return store.WithMaintenanceScope(func(tx *recovery.MaintenanceTx) error {
			s, e := o.bind(ctx, tx)
			if e != nil {
				return e
			}
			return s.planCommitted(store, head, next, prior)
		})
	}})
}
func (o *OfflineLifecycle) ResolvePinTarget(ctx context.Context, scope *recovery.MaintenanceTx, p workingset.PinPolicy) (out string, err error) {
	out = "missing"
	s, e := o.bind(ctx, scope)
	if e != nil {
		return out, e
	}
	ref := p.Selection.Ref
	exists := false
	if ref.Kind == "assertion" {
		e = s.d.Assertions.Read(ctx, func(r *assertions.Reader) error {
			record, e := r.Record(ref.ID)
			if e != nil {
				return e
			}
			state, e := r.State(ref.ID)
			if e != nil {
				return e
			}
			exists = record != nil && !state.Deleted && float64(state.Version) >= ref.Version && float64(state.DeletionEpoch) >= ref.DeletionEpoch && epGet(value(record, "scope"), "kind") == "scene" && epGet(value(record, "scope"), "sceneId") == p.SceneID
			return nil
		})
	} else {
		e = s.d.Commitments.Read(ctx, func(r *commitments.Reader) error {
			head, e := r.Head(ref.ID)
			if e != nil {
				return e
			}
			version, _ := value(head, "latestVersion").(float64)
			epoch, _ := value(head, "deletionEpoch").(float64)
			mutation, _ := value(head, "mutationVersion").(float64)
			exists = head != nil && value(head, "deleted") != true && value(head, "sceneId") == p.SceneID && version >= ref.Version && epoch >= ref.DeletionEpoch && ref.MutationVersion != nil && mutation >= *ref.MutationVersion
			return nil
		})
	}
	if e != nil || !exists {
		return out, e
	}
	out = "stale"
	err = s.d.Commitments.ReadMaintenanceCurrent(ctx, s.d.Assertions, s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
		resolver, e := memoryworkspace.NewResolver(read, s.d.Authority.DateParser)
		if e != nil {
			return e
		}
		values, e := resolver.Resolve(workingset.Context{SceneID: p.SceneID}, []workingset.Reference{ref})
		if e != nil {
			return e
		}
		if len(values) > 0 && values[0].DependencyKey == p.Selection.DependencyKey {
			out = "current"
		}
		return nil
	})
	return
}

// DeleteFingerprint preserves source row order and executes every matching
// delete through the same privacy owner and revision coordinator.
func (o *OfflineLifecycle) DeleteFingerprint(ctx context.Context, fingerprint string) ([]string, error) {
	return o.within(ctx, func(s *offlineFacade, tx *assertions.RevisionTxn) error {
		ids := []string{}
		if e := tx.Store.WithMaintenanceScope(func(scope *recovery.MaintenanceTx) error {
			rows, e := scope.QueryContext(ctx, "SELECT id FROM memories WHERE fingerprint=?", fingerprint)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					return e
				}
				ids = append(ids, id)
			}
			return rows.Err()
		}); e != nil {
			return e
		}
		for _, id := range ids {
			if _, e := s.deletePrivacy(ctx, tx, id); e != nil {
				return e
			}
			privacy, e := s.privacyOwner(ctx, tx)
			if e != nil {
				return e
			}
			if e = privacy.PurgeAssertion(id); e != nil {
				return e
			}
		}
		return tx.Store.WithMaintenanceScope(func(scope *recovery.MaintenanceTx) error {
			_, e := scope.ExecContext(ctx, "INSERT OR IGNORE INTO memory_deleted VALUES(?,?)", fingerprint, func() string {
				if s.options.Assertions.Now != nil {
					return s.options.Assertions.Now()
				}
				return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
			}())
			return e
		})
	})
}
