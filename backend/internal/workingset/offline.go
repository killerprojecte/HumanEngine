package workingset

import (
	"context"
	"errors"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// OfflineMaintenance is an admitted staging-domain owner, not a live reader or
// a reconciler. Supplied replay entries must be the reconciler's already-audited
// original ledger JSON, never a reconstructed policy summary. The adapter
// validates them again but does not append an intent, ACK, or clear quarantine.
type OfflineMaintenance struct {
	owner        *recovery.Maintenance
	tx           *recovery.MaintenanceTx
	bindingError error
	ports        OfflinePorts
}
type OfflinePorts struct {
	ResolvePinTarget func(context.Context, *recovery.MaintenanceTx, PinPolicy) (string, error)
}

func NewOfflineMaintenance(owner *recovery.Maintenance, ports OfflinePorts) (*OfflineMaintenance, error) {
	if owner == nil {
		return nil, errors.New("recovery_maintenance_required")
	}
	return &OfflineMaintenance{owner: owner, ports: ports}, nil
}
func (o *OfflineMaintenance) WithScope(ctx context.Context, tx *recovery.MaintenanceTx) *OfflineMaintenance {
	b := *o
	b.tx = tx
	if tx == nil {
		b.bindingError = errors.New("recovery_maintenance_required")
	} else {
		b.bindingError = tx.CheckBinding(ctx, o.owner)
	}
	return &b
}
func (o *OfflineMaintenance) within(ctx context.Context, fn func(*OfflineMaintenance) error) error {
	if o.bindingError != nil {
		return o.bindingError
	}
	if o.tx != nil {
		if e := o.tx.CheckBinding(ctx, o.owner); e != nil {
			return e
		}
		return fn(o)
	}
	return o.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return fn(o.WithScope(ctx, tx)) })
}
func offlineInvalid() error { return errors.New("recovery_unknown_working_set_schema") }

var offlineTables = []string{"memory_working_set_pins", "memory_working_set_items", "memory_working_set_jobs", "memory_working_set_receipts"}

func offlineColumns(table string) []string {
	switch table {
	case "memory_working_set_pins", "memory_working_set_items", "memory_working_set_jobs":
		return []string{"entity_id", "kind", "scene_id", "value"}
	case "memory_working_set_receipts":
		return []string{"receipt_key", "selected_at", "value"}
	}
	return nil
}
func offlineValidate(table, raw string) error {
	var e error
	switch table {
	case "memory_working_set_pins":
		_, e = Decode[PinPolicy](raw)
	case "memory_working_set_items":
		_, e = Decode[Item](raw)
	case "memory_working_set_jobs":
		_, e = Decode[Job](raw)
	case "memory_working_set_receipts":
		_, e = Decode[Receipt](raw)
	default:
		return offlineInvalid()
	}
	if e != nil {
		return offlineInvalid()
	}
	return nil
}
func (o *OfflineMaintenance) AssertSupported(ctx context.Context) (exists bool, err error) {
	err = o.within(ctx, func(b *OfflineMaintenance) error { var e error; exists, e = b.assertSupported(ctx); return e })
	return
}
func (o *OfflineMaintenance) assertSupported(ctx context.Context) (bool, error) {
	if e := o.tx.CheckBinding(ctx, o.owner); e != nil {
		return false, e
	}
	rows, e := o.tx.QueryContext(ctx, "SELECT name,type FROM main.sqlite_master WHERE (type='table' OR type='view') AND name GLOB 'memory_working_set_*'")
	if e != nil {
		return false, e
	}
	type table struct{ name, kind string }
	tables := []table{}
	for rows.Next() {
		var v table
		if e = rows.Scan(&v.name, &v.kind); e != nil {
			break
		}
		tables = append(tables, v)
	}
	if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
		return false, e
	}
	for _, table := range tables {
		expected := offlineColumns(table.name)
		if table.kind != "table" || expected == nil {
			return false, offlineInvalid()
		}
		rows, e := o.tx.QueryContext(ctx, `PRAGMA main.table_xinfo("`+table.name+`")`)
		if e != nil {
			return false, e
		}
		columns := []string{}
		for rows.Next() {
			var cid, notnull, pk, hidden int
			var name, typ string
			var dflt any
			if e = rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk, &hidden); e != nil {
				break
			}
			columns = append(columns, name)
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return false, e
		}
		sort.Strings(columns)
		if len(columns) != len(expected) {
			return false, offlineInvalid()
		}
		for i, col := range columns {
			if col != expected[i] {
				return false, offlineInvalid()
			}
		}
		rows, e = o.tx.QueryContext(ctx, `SELECT value FROM main."`+table.name+`"`)
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var value any
			if e = rows.Scan(&value); e != nil {
				break
			}
			raw, ok := value.(string)
			if !ok {
				e = offlineInvalid()
				break
			}
			if e = offlineValidate(table.name, raw); e != nil {
				break
			}
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return false, e
		}
		if table.name == "memory_working_set_pins" {
			rows, e = o.tx.QueryContext(ctx, "SELECT scene_id,kind,entity_id,value FROM main.memory_working_set_pins")
			if e != nil {
				return false, e
			}
			for rows.Next() {
				var scene, kind, id, value any
				if e = rows.Scan(&scene, &kind, &id, &value); e != nil {
					break
				}
				raw, ok := value.(string)
				if !ok {
					e = offlineInvalid()
					break
				}
				p, parseErr := Decode[PinPolicy](raw)
				if parseErr != nil || scene != p.SceneID || kind != p.Selection.Ref.Kind || id != p.Selection.Ref.ID {
					e = offlineInvalid()
					break
				}
			}
			if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
				return false, e
			}
		}
	}
	// The source checks the four-table census after validating each table's rows.
	if len(tables) > 0 && len(tables) != 4 {
		return false, offlineInvalid()
	}
	return len(tables) > 0, nil
}
func (o *OfflineMaintenance) Reset(ctx context.Context) error {
	return o.within(ctx, func(b *OfflineMaintenance) error {
		yes, e := b.assertSupported(ctx)
		if e != nil || !yes {
			return e
		}
		for _, table := range offlineTables[1:] {
			if _, e = b.tx.ExecContext(ctx, `DELETE FROM main."`+table+`"`); e != nil {
				return e
			}
		}
		for _, table := range offlineTables[1:] {
			rows, e := b.tx.QueryContext(ctx, `SELECT 1 FROM main."`+table+`" LIMIT 1`)
			if e != nil {
				return e
			}
			found := rows.Next()
			e = errors.Join(rows.Err(), rows.Close())
			if e != nil {
				return e
			}
			if found {
				return errors.New("recovery_working_set_reset_incomplete")
			}
		}
		return nil
	})
}
func pinKey(p PinPolicy) string {
	v, _ := jsjson.Stringify([]any{p.SceneID, p.Selection.Ref.Kind, p.Selection.Ref.ID})
	return v
}
func commitmentDeleteKey(scene, id string) string {
	v, _ := jsjson.Stringify([]any{scene, id})
	return v
}

// VerifyPins preserves first-key insertion order while retaining the last policy
// for each entity. Any terminal delete suppresses its policy regardless of the
// event's position. Repeated raw parsing is explicit current composition cost;
// the future reconciler may share audited events without inventing authority.
func (o *OfflineMaintenance) VerifyPins(ctx context.Context, rawEvents []string) error {
	return o.within(ctx, func(b *OfflineMaintenance) error { return b.verifyPins(ctx, rawEvents) })
}
func (o *OfflineMaintenance) verifyPins(ctx context.Context, rawEvents []string) error {
	if _, e := o.assertSupported(ctx); e != nil {
		return e
	}
	policies := map[string]PinPolicy{}
	order := []string{}
	deletedAssertions := map[string]bool{}
	deletedCommitments := map[string]bool{}
	for _, raw := range rawEvents {
		if e := recovery.ValidateEvent(raw); e != nil {
			return e
		}
		value, e := jsjson.Parse(raw)
		if e != nil {
			return e
		}
		event, ok := value.(*jsjson.Object)
		if !ok {
			return errors.New("recovery_write_invalid")
		}
		kind, _ := event.Get("kind")
		switch kind {
		case "assertion":
			terminal, _ := event.Get("terminal")
			if terminal == true {
				id, _ := event.Get("id")
				deletedAssertions[id.(string)] = true
			}
		case "commitment_delete":
			scene, _ := event.Get("sceneId")
			id, _ := event.Get("id")
			deletedCommitments[commitmentDeleteKey(scene.(string), id.(string))] = true
		case "working_set_pin":
			value, _ := event.Get("policy")
			raw, e := jsjson.Stringify(value)
			if e != nil {
				return e
			}
			p, e := Decode[PinPolicy](raw)
			if e != nil {
				return e
			}
			key := pinKey(p)
			if _, ok := policies[key]; !ok {
				order = append(order, key)
			}
			policies[key] = p
		}
	}
	exists, e := o.assertSupported(ctx)
	if e != nil {
		return e
	}
	if exists {
		rows, e := o.tx.QueryContext(ctx, "SELECT value FROM main.memory_working_set_pins")
		if e != nil {
			return e
		}
		for rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				break
			}
			p, parseErr := Decode[PinPolicy](raw)
			if parseErr != nil {
				e = parseErr
				break
			}
			if _, ok := policies[pinKey(p)]; !ok {
				e = errors.New("recovery_working_set_pin_authority_missing")
				break
			}
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return e
		}
	}
	for _, key := range order {
		p := policies[key]
		ref := p.Selection.Ref
		deleted := deletedAssertions[ref.ID]
		if ref.Kind == "commitment" {
			deleted = deletedCommitments[commitmentDeleteKey(p.SceneID, ref.ID)]
		}
		if deleted {
			yes, e := o.assertSupported(ctx)
			if e != nil {
				return e
			}
			if yes {
				if _, e = o.tx.ExecContext(ctx, "DELETE FROM main.memory_working_set_pins WHERE scene_id=? AND kind=? AND entity_id=?", bind([]any{p.SceneID, ref.Kind, ref.ID})...); e != nil {
					return e
				}
			}
			continue
		}
		if p.Pinned {
			target := ""
			var e error
			if o.ports.ResolvePinTarget != nil {
				target, e = o.ports.ResolvePinTarget(ctx, o.tx, p)
				if e != nil {
					return e
				}
			}
			if target != "current" && target != "stale" {
				return errors.New("recovery_working_set_pin_material_missing")
			}
		}
		if e := o.tx.EnsureWorkingSetSchema(ctx); e != nil {
			return e
		}
		raw, e := Encode(p)
		if e != nil {
			return e
		}
		if _, e = o.tx.ExecContext(ctx, "INSERT INTO main.memory_working_set_pins VALUES(?,?,?,?) ON CONFLICT(scene_id,kind,entity_id) DO UPDATE SET value=excluded.value", bind([]any{p.SceneID, ref.Kind, ref.ID, raw})...); e != nil {
			return e
		}
	}
	return nil
}

// ResetInScope and VerifyPinsInScope expose the source's transaction-required
// entry points. The root Reset/VerifyPins convenience methods own Apply instead.
// Neither scoped method silently opens a transaction for an unbound caller.
func (o *OfflineMaintenance) ResetInScope(ctx context.Context) error {
	if o.tx == nil {
		if o.bindingError != nil {
			return o.bindingError
		}
		return errors.New("recovery_transaction_required")
	}
	return o.Reset(ctx)
}
func (o *OfflineMaintenance) VerifyPinsInScope(ctx context.Context, events []string) error {
	if o.tx == nil {
		if o.bindingError != nil {
			return o.bindingError
		}
		return errors.New("recovery_transaction_required")
	}
	return o.VerifyPins(ctx, events)
}
