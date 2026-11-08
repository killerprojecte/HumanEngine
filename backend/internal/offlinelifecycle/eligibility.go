package offlinelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type object = jsjson.Object

func get(o *object, key string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(key)
	return v
}
func str(v any) string  { s, _ := v.(string); return s }
func obj(v any) *object { o, _ := v.(*object); return o }
func array(v any) []any { a, _ := v.([]any); return a }
func event(raw string) (*object, error) {
	if e := recovery.ValidateEvent(raw); e != nil {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	return obj(v), e
}

type eligibilityObligation struct {
	sequence int64
	change   *object
}
type eligibilityReplay struct {
	owner    *recovery.Maintenance
	prefix   int64
	terminal map[string]bool
	latest   map[string]eligibilityObligation
	negative map[string]*object
	order    []string
}

func newEligibility(owner *recovery.Maintenance, entries []recovery.RecoveryEntry) (*eligibilityReplay, error) {
	p := &eligibilityReplay{owner: owner, prefix: owner.SnapshotSequence(), terminal: map[string]bool{}, latest: map[string]eligibilityObligation{}, negative: map[string]*object{}}
	for _, entry := range entries {
		v, e := event(entry.Payload)
		if e != nil {
			return nil, e
		}
		if get(v, "kind") == "assertion" && get(v, "terminal") == true {
			p.terminal[str(get(v, "id"))] = true
		}
		if get(v, "kind") == "assertion_eligibility" {
			for _, value := range array(get(v, "changes")) {
				change := obj(value)
				p.latest[str(get(change, "id"))] = eligibilityObligation{entry.Sequence, change}
			}
		}
	}
	return p, nil
}
func readRecord(ctx context.Context, tx *recovery.MaintenanceTx, id string) (*object, error) {
	var raw string
	e := tx.QueryRowContext(ctx, "SELECT value FROM memories WHERE id=?", id).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	o, ok := v.(*object)
	if !ok {
		return nil, errors.New("recovery_eligibility_material_invalid")
	}
	return o, nil
}
func (p *eligibilityReplay) Apply(ctx context.Context, entry recovery.RecoveryEntry) error {
	if entry.Sequence <= p.prefix {
		return nil
	}
	v, e := event(entry.Payload)
	if e != nil {
		return e
	}
	if get(v, "kind") != "assertion_eligibility" {
		return errors.New("recovery_eligibility_event_invalid")
	}
	applied := []*object{}
	e = p.owner.Apply(ctx, 11, func(tx *recovery.MaintenanceTx) error {
		for _, value := range array(get(v, "changes")) {
			change := obj(value)
			id := str(get(change, "id"))
			if p.terminal[id] || p.latest[id].sequence != entry.Sequence {
				continue
			}
			current, e := readRecord(ctx, tx, id)
			if e != nil {
				return e
			}
			if current == nil {
				continue
			}
			incarnation, e := assertions.EligibilityIncarnation(current)
			if e != nil {
				return e
			}
			if incarnation != get(change, "incarnationHash") {
				return errors.New("recovery_eligibility_incarnation_mismatch")
			}
			state := obj(get(change, "state"))
			denied := get(state, "status") == "invalidated" || get(state, "lineageStatus") == "quarantined"
			if !denied {
				proofs := any([]any{})
				var raw string
				e := tx.QueryRowContext(ctx, "SELECT value FROM memory_supports WHERE memory_id=?", id).Scan(&raw)
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				if e == nil {
					proofs, e = jsjson.Parse(raw)
					if e != nil {
						return e
					}
				}
				material, e := assertions.EligibilityMaterial(current, proofs)
				if e != nil {
					return e
				}
				if material != get(change, "materialHash") {
					return errors.New("recovery_eligibility_grant_material_missing")
				}
			}
			next := current.Clone()
			if denied {
				if get(state, "status") == "invalidated" {
					_ = next.Set("status", "invalidated")
				}
				if get(state, "lineageStatus") == "quarantined" {
					_ = next.Set("lineageStatus", "quarantined")
				}
				applied = append(applied, change)
			} else {
				_ = next.Set("status", get(state, "status"))
				if get(state, "lineageStatus") == nil {
					next.Delete("lineageStatus")
				} else {
					_ = next.Set("lineageStatus", get(state, "lineageStatus"))
				}
			}
			raw, e := jsjson.Stringify(next)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE memories SET value=? WHERE id=?", raw, id); e != nil {
				return e
			}
			if denied {
				if _, e = tx.ExecContext(ctx, "DELETE FROM memory_vectors WHERE memory_id=?", id); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if e != nil {
		return e
	}
	for _, change := range applied {
		id := str(get(change, "id"))
		if _, ok := p.negative[id]; !ok {
			p.order = append(p.order, id)
		}
		p.negative[id] = change
	}
	return nil
}
func (p *eligibilityReplay) Verify(ctx context.Context, tx *recovery.MaintenanceTx) error {
	if e := tx.CheckBinding(ctx, p.owner); e != nil {
		return e
	}
	for _, id := range p.order {
		change := p.negative[id]
		current, e := readRecord(ctx, tx, id)
		if e != nil {
			return e
		}
		if current == nil || p.terminal[id] {
			continue
		}
		incarnation, e := assertions.EligibilityIncarnation(current)
		if e != nil {
			return e
		}
		state := obj(get(change, "state"))
		if incarnation != get(change, "incarnationHash") || (get(state, "status") == "invalidated" && get(current, "status") != "invalidated") || (get(state, "lineageStatus") == "quarantined" && get(current, "lineageStatus") != "quarantined") {
			return errors.New("recovery_eligibility_withdrawal_incomplete")
		}
	}
	return nil
}
