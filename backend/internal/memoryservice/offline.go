package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryworkspace"
	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

// OfflineLifecycle is explicitly staging-only. It owns no live writer, runtime
// listener, scheduler or authority ACK. Schema initialization and ordered ledger
// reconciliation are separate owners; construct only after their admission.
type OfflineLifecycle struct {
	owner   *recovery.Maintenance
	options OfflineLifecycleOptions
}
type OfflineLifecycleOptions struct {
	Assertions         assertions.Options
	Authority          assertionauthority.Options
	Commitments        commitments.Options
	Projection         projections.Options
	WorkingSet         workingset.Ports
	ProjectionEnabled  func() bool
	CommitmentsEnabled func(commitments.Context) (bool, error)
	Log                func(string, string, string)
}
type offlineFacade struct {
	d             Dependencies
	ctx           context.Context
	owner         *recovery.Maintenance
	options       OfflineLifecycleOptions
	notifications *NotificationQueue
}

func NewOfflineLifecycle(owner *recovery.Maintenance, options OfflineLifecycleOptions) (*OfflineLifecycle, error) {
	if owner == nil || options.Assertions.DateParser == nil || options.Authority.DateParser == nil || options.Authority.Compare == nil || options.Authority.NowMillis == nil || options.Commitments.Compare == nil || options.Commitments.DateParser == nil || options.ProjectionEnabled == nil || options.CommitmentsEnabled == nil || options.Log == nil {
		return nil, errors.New("offline_lifecycle_dependencies_required")
	}
	return &OfflineLifecycle{owner, options}, nil
}
func (o *OfflineLifecycle) within(ctx context.Context, fn func(*offlineFacade, *assertions.RevisionTxn) error) ([]string, error) {
	return o.owner.CaptureEffects(ctx, func(scope *recovery.MaintenanceTx) error { return o.inScope(ctx, scope, fn) })
}
func (o *OfflineLifecycle) inScope(ctx context.Context, scope *recovery.MaintenanceTx, fn func(*offlineFacade, *assertions.RevisionTxn) error) error {
	s, e := o.bind(ctx, scope)
	if e != nil {
		return e
	}
	revisions, e := assertions.NewCoordinator(s.d.Assertions, assertions.CoordinatorOptions{HistorySupports: s.historySupports, Retain: s.retain, RemoveProjection: func(st *assertions.Store, id string) error {
		if e := s.removeProjection(st, id); e != nil {
			return e
		}
		return s.unavailable(st, id)
	}, Committed: s.committed, AfterCommit: func() error {
		_, e := s.d.Projections.Flush(ctx)
		if e != nil {
			s.d.Log("warn", "memory", "memory_projection_refresh_failed")
		}
		return nil
	}})
	if e != nil {
		return e
	}
	return revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error { return fn(s, tx) })
}
func (o *OfflineLifecycle) bind(ctx context.Context, tx *recovery.MaintenanceTx) (*offlineFacade, error) {
	s := &offlineFacade{ctx: ctx, owner: o.owner, options: o.options, notifications: &NotificationQueue{pending: map[string]notification{}}}
	s.d.Authority = o.options.Authority
	s.d.CommitmentsEnabled = o.options.CommitmentsEnabled
	s.d.Log = o.options.Log
	var e error
	s.d.Assertions, e = assertions.NewMaintenanceStore(o.owner, tx, ctx, o.options.Assertions)
	if e != nil {
		return nil, e
	}
	relations, e := relationstorage.NewMaintenanceStore(o.owner, tx, ctx)
	if e != nil {
		return nil, e
	}
	s.d.Relations = relations
	k, e := knowledge.NewMaintenanceStore(o.owner, tx, ctx)
	if e != nil {
		return nil, e
	}
	s.d.Knowledge = &knowledge.Service{Storage: k}
	s.d.Semantic, e = semantic.NewMaintenanceStore(o.owner, tx, ctx)
	if e != nil {
		return nil, e
	}
	plans, e := commitments.NewOfflineMaintenance(o.owner, o.options.Commitments, commitments.Owners{AfterWrite: s.planCommitted})
	if e != nil {
		return nil, e
	}
	privacy, e := plans.BindScope(ctx, tx)
	if e != nil {
		return nil, e
	}
	s.d.Commitments = privacy.Store
	ports := o.options.WorkingSet
	ports.ResolveCurrent = func(scope *workingset.Scope, c workingset.Context, refs []workingset.Reference) (out []workingset.Snapshot, err error) {
		child := scope.MaintenanceTx()
		if child == nil {
			return nil, errors.New("offline_lifecycle_scope_required")
		}
		err = s.d.Commitments.WithMaintenanceTx(ctx, child).ReadMaintenanceCurrent(ctx, s.d.Assertions.WithMaintenanceTx(ctx, child), s.d.CommitmentsEnabled, s.d.Authority, func(read *commitments.ReadService) error {
			r, e := memoryworkspace.NewResolver(read, s.d.Authority.DateParser)
			if e != nil {
				return e
			}
			out, e = r.Resolve(c, refs)
			return e
		})
		return
	}
	s.d.WorkingSet, e = workingset.NewMaintenanceService(o.owner, tx, ctx, ports)
	if e != nil {
		return nil, e
	}
	s.d.Projections, e = projections.NewMaintenanceCoordinator(o.owner, tx, ctx, o.options.ProjectionEnabled, func(child *recovery.MaintenanceTx, id string) (record *Object, err error) {
		err = s.d.Assertions.WithMaintenanceTx(ctx, child).Read(ctx, func(r *assertions.Reader) error {
			v, e := r.Record(id)
			if e != nil || v == nil || truthy(value(v, "internalUse")) {
				return e
			}
			a, e := assertionauthority.New(r, s.d.Authority)
			if e != nil {
				return e
			}
			ok, e := a.Supported(v)
			if ok {
				record = v
			}
			return e
		})
		return
	}, o.options.Projection)
	if e != nil {
		return nil, e
	}
	return s, nil
}
func (o *OfflineLifecycle) RedactRecoveredHistory(ctx context.Context, id string) ([]string, error) {
	return o.within(ctx, func(s *offlineFacade, tx *assertions.RevisionTxn) error {
		p, e := assertionlifecycle.New(tx, s.owners(), s.d.Authority)
		if e != nil {
			return e
		}
		if e = p.PurgeHistoryDependents(id, nil, nil); e != nil {
			return e
		}
		if e = s.purgeRelations(tx.Store, id); e != nil {
			return e
		}
		if _, e = tx.Store.Purge(id); e != nil {
			return e
		}
		var record *Object
		if e = tx.Store.Read(ctx, func(r *assertions.Reader) error { var e error; record, e = r.Record(id); return e }); e != nil {
			return e
		}
		if record != nil {
			if e = tx.Touch(id); e != nil {
				return e
			}
		}
		return tx.Transaction(ctx, s.refresh)
	})
}
func (o *OfflineLifecycle) EraseRecoveredAssertion(ctx context.Context, id string) ([]string, error) {
	return o.within(ctx, func(s *offlineFacade, tx *assertions.RevisionTxn) error {
		var record *Object
		if e := tx.Store.Read(ctx, func(r *assertions.Reader) error { var e error; record, e = r.Record(id); return e }); e != nil {
			return e
		}
		if record != nil {
			return tx.Transaction(ctx, func(child *assertions.RevisionTxn) error { _, e := s.deletePrivacy(ctx, child, id); return e })
		}
		p, e := assertionlifecycle.New(tx, s.owners(), s.d.Authority)
		if e != nil {
			return e
		}
		if e = p.PurgeHistoryDependents(id, nil, nil); e != nil {
			return e
		}
		if e = s.purgeRelations(tx.Store, id); e != nil {
			return e
		}
		privacy, e := s.privacyOwner(ctx, tx)
		if e != nil {
			return e
		}
		if e = tx.Store.WithMaintenanceScope(func(scope *recovery.MaintenanceTx) error {
			return privacy.WithMaintenanceTx(ctx, scope).PurgeAssertion(id)
		}); e != nil {
			return e
		}
		if e = s.terminalPrivacy(tx, id); e != nil {
			return e
		}
		if _, e = tx.Store.Erase(id); e != nil {
			return e
		}
		if e = p.InvalidateDependents(id, nil); e != nil {
			return e
		}
		return tx.Transaction(ctx, s.refresh)
	})
}
func (s *offlineFacade) mutations(tx *recovery.MaintenanceTx) *workingset.MutationHooks {
	return s.d.WorkingSet.WithMaintenanceTx(s.ctx, tx).Mutations(workingset.MutationPorts{})
}
func (s *offlineFacade) unavailable(st *assertions.Store, id string) error {
	return st.WithMaintenanceScope(func(tx *recovery.MaintenanceTx) error { _, e := s.mutations(tx).InvalidateAssertion(id); return e })
}
func (s *offlineFacade) removeProjection(st *assertions.Store, id string) error {
	return st.WithMaintenanceScope(func(tx *recovery.MaintenanceTx) error { return s.d.Projections.WithMaintenanceTx(s.ctx, tx).Remove(id) })
}
func (s *offlineFacade) projectionCommitted(st *assertions.Store, id string, version, epoch int64) error {
	return st.WithMaintenanceScope(func(tx *recovery.MaintenanceTx) error {
		return s.d.Projections.WithMaintenanceTx(s.ctx, tx).Committed(id, version, epoch)
	})
}

// Source recovery constructs MemoryService without attaching runtime listeners.
// Use its actual queue: the zero-listener branch never schedules external work.
func (s *offlineFacade) notify(ctx context.Context, st *assertions.Store, scene string, ref Reference) error {
	return s.notifications.AssertionCommitted(scene, ref)
}
