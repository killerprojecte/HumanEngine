package commitments

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sort"
)

// OfflineMaintenance is one admitted staging domain owner. It does not read the
// authority through an independent connection, invent replay events, ACK a head,
// or transition quarantine to ready. Supplied events must be the reconciler's
// already-audited original ledger entries, not a reconstructed state summary.
type OfflineMaintenance struct {
	scope       *recovery.MaintenanceTx
	scopeError  error
	owner       *recovery.Maintenance
	options     Options
	owners      Owners
	ownership   chan struct{}
	initialized bool
}

func NewOfflineMaintenance(owner *recovery.Maintenance, options Options, owners Owners) (*OfflineMaintenance, error) {
	if owner == nil {
		return nil, errors.New("commitment_maintenance_required")
	}
	if options.Compare == nil || options.DateParser == nil {
		return nil, errors.New("commitment_time_collation_required")
	}
	if owners.AfterWrite == nil {
		return nil, errors.New("commitment_owner_required")
	}
	o := &OfflineMaintenance{owner: owner, options: options, owners: owners, ownership: make(chan struct{}, 1)}
	o.ownership <- struct{}{}
	return o, nil
}

// Ownership spans source's audit/construction/effect phases while respecting
// cancellation of a queued caller. It is serialization, not an admission cap.
func (o *OfflineMaintenance) acquire(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-o.ownership:
	}
	if e := ctx.Err(); e != nil {
		o.release()
		return e
	}
	return nil
}
func (o *OfflineMaintenance) release() { o.ownership <- struct{}{} }
func (o *OfflineMaintenance) within(ctx context.Context, fn func(*PrivacyService) error) error {
	if o.scopeError != nil {
		return o.scopeError
	}
	if o.scope != nil {
		if e := o.scope.CheckBinding(ctx, o.owner); e != nil {
			return e
		}
		store := &Store{options: o.options, maintenance: &maintenanceScope{o.owner, o.scope, ctx}}
		service, e := NewPrivacyService(store, o.owners)
		if e != nil {
			return e
		}
		return fn(service)
	}
	return o.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		store := &Store{options: o.options, maintenance: &maintenanceScope{o.owner, tx, ctx}}
		service, e := NewPrivacyService(store, o.owners)
		if e != nil {
			return e
		}
		return fn(service)
	})
}

var knownCommitmentColumns = map[string][]string{
	"memory_commitment_heads":            {"id", "scene_id", "value"},
	"memory_commitment_revisions":        {"id", "version", "value"},
	"memory_commitment_commands":         {"key", "id", "version"},
	"memory_commitment_transitions":      {"id", "mutation_version", "scene_id", "value"},
	"memory_commitment_temporal_windows": {"id", "scene_id", "value"},
}

func (s *Store) assertSupported() (bool, error) {
	if s.maintenance == nil {
		return false, errors.New("recovery_maintenance_required")
	}
	if e := s.ready(); e != nil {
		return false, e
	}
	r := s.reader()
	rows, e := r.query("SELECT name,type FROM sqlite_master WHERE (type='table' OR type='view') AND name LIKE 'memory_commitment_%'")
	if e != nil {
		return false, e
	}
	type table struct{ name, kind string }
	tables := []table{}
	for rows.Next() {
		var t table
		if e = rows.Scan(&t.name, &t.kind); e != nil {
			_ = rows.Close()
			return false, e
		}
		tables = append(tables, t)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return false, e
	}
	seen := map[string]bool{}
	for _, t := range tables {
		expected, ok := knownCommitmentColumns[t.name]
		if !ok || t.kind != "table" {
			return false, errors.New("recovery_unknown_commitment_schema")
		}
		rows, e := r.query(`PRAGMA table_xinfo("` + t.name + `")`)
		if e != nil {
			return false, e
		}
		columns := []string{}
		for rows.Next() {
			var cid, notnull, pk, hidden int
			var name, kind string
			var defaultValue any
			if e = rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk, &hidden); e != nil {
				_ = rows.Close()
				return false, e
			}
			columns = append(columns, name)
		}
		if e = errors.Join(rows.Err(), rows.Close()); e != nil {
			return false, e
		}
		want := append([]string{}, expected...)
		sort.Strings(want)
		sort.Strings(columns)
		if len(columns) != len(want) {
			return false, errors.New("recovery_unknown_commitment_schema")
		}
		for i, c := range columns {
			if c != want[i] {
				return false, errors.New("recovery_unknown_commitment_schema")
			}
		}
		seen[t.name] = true
	}
	if len(tables) > 0 && ((len(tables) != 3 && len(tables) != 5) || !seen["memory_commitment_heads"] || !seen["memory_commitment_revisions"] || !seen["memory_commitment_commands"]) {
		return false, errors.New("recovery_unknown_commitment_schema")
	}
	if len(tables) == 5 {
		ids, e := r.AllTemporalIDs()
		if e != nil {
			return false, e
		}
		for _, id := range ids {
			if _, e = r.Timeline(id); e != nil {
				return false, e
			}
		}
	}
	return len(tables) > 0, nil
}
func (o *OfflineMaintenance) assertSupported(ctx context.Context) (exists bool, err error) {
	err = o.within(ctx, func(service *PrivacyService) error {
		var e error
		exists, e = service.Store.assertSupported()
		return e
	})
	return
}
func (o *OfflineMaintenance) AssertSupported(ctx context.Context) (bool, error) {
	if e := o.acquire(ctx); e != nil {
		return false, e
	}
	defer o.release()
	return o.assertSupported(ctx)
}

// Source existing() performs lazy construction BEFORE the method's mutation
// transaction. Preserve its independently committed index repair, unless the
// caller already has an outer transaction (Apply then owns only savepoints).
func (o *OfflineMaintenance) existing(ctx context.Context) (bool, error) {
	exists, e := o.assertSupported(ctx)
	if e != nil || !exists {
		return false, e
	}
	if !o.initialized {
		if o.scope != nil {
			e = o.scope.EnsureCommitmentStorageSchema(ctx)
		} else {
			e = o.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return tx.EnsureCommitmentStorageSchema(ctx) })
		}
		if e != nil {
			return false, e
		}
		o.initialized = true
	}
	return true, nil
}
func (o *OfflineMaintenance) Recall(ctx context.Context, scene, message string) error {
	if e := o.acquire(ctx); e != nil {
		return e
	}
	defer o.release()
	exists, e := o.existing(ctx)
	if e != nil || !exists {
		return e
	}
	return o.within(ctx, func(s *PrivacyService) error { return s.PurgeSource(scene, message) })
}
func (o *OfflineMaintenance) PurgeAssertion(ctx context.Context, id string) error {
	if e := o.acquire(ctx); e != nil {
		return e
	}
	defer o.release()
	exists, e := o.existing(ctx)
	if e != nil || !exists {
		return e
	}
	return o.within(ctx, func(s *PrivacyService) error { return s.PurgeAssertion(id) })
}
func replayEvent(raw string, kind string) (*Object, error) {
	if e := recovery.ValidateEvent(raw); e != nil {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	event := object(v)
	if kind != "" && get(event, "kind") != kind {
		return nil, errors.New("recovery_commitment_event_invalid")
	}
	return event, nil
}
func (o *OfflineMaintenance) RecoverErase(ctx context.Context, raw string) error {
	if e := o.acquire(ctx); e != nil {
		return e
	}
	defer o.release()
	event, e := replayEvent(raw, "commitment_delete")
	if e != nil {
		return e
	}
	exists, e := o.existing(ctx)
	if e != nil || !exists {
		return e
	}
	return o.within(ctx, func(s *PrivacyService) error {
		return s.recoverErase(str(get(event, "id")), str(get(event, "sceneId")), num(get(event, "deletionEpoch")))
	})
}
func (o *OfflineMaintenance) ReplayMarker(ctx context.Context, raw string) error {
	if e := o.acquire(ctx); e != nil {
		return e
	}
	defer o.release()
	event, e := replayEvent(raw, "commitment_head")
	if e != nil {
		return e
	}
	if !has(event, "privacyMarker") {
		return nil
	}
	exists, e := o.existing(ctx)
	if e != nil {
		return e
	}
	if !exists {
		return errors.New("recovery_commitment_material_missing")
	}
	e = o.within(ctx, func(s *PrivacyService) error { return s.recoverTemporalMarker(object(get(event, "privacyMarker"))) })
	if e != nil && e.Error() == "commitment_temporal_marker_unavailable" {
		return errors.New("recovery_commitment_material_missing")
	}
	return e
}

// Verify intentionally does not ACK the events. Deleted IDs are checked first,
// in first-insertion order, using their last event value; any deleted event
// suppresses latest-head verification for that ID, independent of event order.
func (o *OfflineMaintenance) Verify(ctx context.Context, rawEvents []string) error {
	if e := o.acquire(ctx); e != nil {
		return e
	}
	defer o.release()
	exists, e := o.existing(ctx)
	if e != nil {
		return e
	}
	latest, deleted := map[string]*Object{}, map[string]*Object{}
	latestOrder, deletedOrder := []string{}, []string{}
	for _, raw := range rawEvents {
		event, e := replayEvent(raw, "")
		if e != nil {
			return e
		}
		id := str(get(event, "id"))
		switch get(event, "kind") {
		case "commitment_head":
			if _, ok := latest[id]; !ok {
				latestOrder = append(latestOrder, id)
			}
			latest[id] = event
		case "commitment_delete":
			if _, ok := deleted[id]; !ok {
				deletedOrder = append(deletedOrder, id)
			}
			deleted[id] = event
		}
	}
	return o.within(ctx, func(service *PrivacyService) error {
		reader := service.Store.reader()
		snapshot := func(id string) (*Object, error) {
			if !exists {
				return nil, nil
			}
			return reader.Snapshot(id)
		}
		for _, id := range deletedOrder {
			event := deleted[id]
			s, e := snapshot(id)
			if e != nil {
				return e
			}
			head := get(s, "head")
			if s != nil && (get(head, "sceneId") != get(event, "sceneId") || num(get(head, "deletionEpoch")) < num(get(event, "deletionEpoch")) || get(head, "deleted") != true || len(arr(get(s, "revisions"))) > 0 || get(s, "timeline") != nil) {
				return errors.New("recovery_commitment_purge_incomplete")
			}
			if exists {
				var one int
				found, e := reader.one("SELECT 1 FROM memory_commitment_commands WHERE id=?", []any{id}, &one)
				if e != nil {
					return e
				}
				if found {
					return errors.New("recovery_commitment_purge_incomplete")
				}
			}
		}
		for _, id := range latestOrder {
			if _, ok := deleted[id]; ok {
				continue
			}
			event := latest[id]
			s, e := snapshot(id)
			if e != nil {
				return e
			}
			if s == nil {
				return errors.New("recovery_commitment_material_missing")
			}
			head := get(s, "head")
			if get(head, "deleted") == true || get(head, "sceneId") != get(event, "sceneId") || get(head, "latestVersion") != get(event, "termsVersion") || get(head, "mutationVersion") != get(event, "mutationVersion") || get(head, "deletionEpoch") != get(event, "deletionEpoch") || (get(event, "schemaVersion") == float64(2) && get(s, "timeline") == nil) || get(s, "qualificationHash") != get(event, "qualificationHash") {
				return errors.New("recovery_commitment_head_mismatch")
			}
		}
		return nil
	})
}
func (s *PrivacyService) recoverTemporalMarker(raw *Object) error {
	if s.Store.maintenance == nil {
		return errors.New("commitment_maintenance_required")
	}
	if e := s.Store.ready(); e != nil {
		return e
	}
	marker, e := NormalizeMarker(raw)
	if e != nil {
		return e
	}
	mh := get(marker, "head")
	id := str(get(mh, "id"))
	head, e := s.Store.reader().Head(id)
	if e != nil {
		return e
	}
	if head == nil || get(head, "sceneId") != get(mh, "sceneId") {
		return errors.New("commitment_temporal_marker_unavailable")
	}
	if get(head, "deleted") == true {
		return nil
	}
	if get(head, "deletionEpoch") != get(mh, "deletionEpoch") || num(get(head, "mutationVersion")) < num(get(mh, "mutationVersion")) {
		return errors.New("commitment_temporal_marker_unavailable")
	}
	old, e := s.Store.reader().Timeline(id)
	if e != nil {
		return e
	}
	transition := obj("schemaVersion", float64(1), "id", id, "sceneId", get(head, "sceneId"), "mutationVersion", get(mh, "mutationVersion"), "recordedAt", get(marker, "recordedAt"), "reason", get(marker, "reason"), "head", mh, "states", nil)
	for _, previous := range arr(get(old, "transitions")) {
		if get(previous, "mutationVersion") == get(transition, "mutationVersion") {
			if !deepEqual(previous, transition) {
				return errors.New("commitment_temporal_marker_conflict")
			}
			return nil
		}
	}
	if old != nil && num(get(transition, "mutationVersion")) <= num(get(get(old, "window"), "prunedThroughMutationVersion")) {
		return nil
	}
	timeline, e := AppendTimeline(old, transition, s.Store.options.DateParser)
	if e != nil {
		return e
	}
	return s.Store.ReplaceTimeline(id, timeline)
}
func (s *PrivacyService) recoverErase(id, scene string, epoch float64) error {
	if s.Store.maintenance == nil {
		return errors.New("commitment_maintenance_required")
	}
	if e := s.Store.ready(); e != nil {
		return e
	}
	head, e := s.Store.reader().Head(id)
	if e != nil || head == nil {
		return e
	}
	if get(head, "sceneId") != scene {
		return errors.New("commitment_scope_mismatch")
	}
	if get(head, "deleted") == true {
		if e = s.Store.ReplaceTimeline(id, nil); e != nil {
			return e
		}
		return s.Store.Clear(id)
	}
	head = head.Clone()
	nextEpoch := num(get(head, "deletionEpoch")) + 1
	if epoch > nextEpoch {
		nextEpoch = epoch
	}
	_ = head.Set("deleted", true)
	_ = head.Set("deletionEpoch", nextEpoch)
	_ = head.Set("mutationVersion", num(get(head, "mutationVersion"))+1)
	return s.write("delete", head, []any{}, nil)
}

// WithScope borrows final reconciliation's transaction. It neither opens a
// second root transaction nor acquires the maintenance owner recursively.
func (o *OfflineMaintenance) WithScope(ctx context.Context, tx *recovery.MaintenanceTx) *OfflineMaintenance {
	next := *o
	next.scope = tx
	if tx == nil {
		next.scopeError = errors.New("recovery_transaction_required")
	} else {
		next.scopeError = tx.CheckBinding(ctx, o.owner)
	}
	return &next
}
