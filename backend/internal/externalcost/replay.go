package externalcost

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Maintenance is an already-authorized offline capability supplied by the
// caller, NOT a global recovery entrypoint. Implementations must admit a
// quarantined staging database against an independently verified authority
// (path containment/symlinks, instance, reader capability).
// ApplyReceipt runs fn in a per-receipt immediate transaction (nested savepoint
// if already inside a caller transaction), requiring operational reader 10 and
// upgrading minimumReader inside that transaction. A failing receipt rolls
// back without undoing earlier commits. ReadStorage is the SAME staging DB.
// Never construct this from diagnostic Authority.Head or ordinary recovery.Write.
// The global maintenance orchestrator is not yet migrated. No default exists.
type Maintenance interface {
	ReadStorage() Storage
	ApplyReceipt(context.Context, func(Storage) error) error
}

// ReplayOffline accepts complete verified authority history. Success proves
// only this domain's exact heads, never global readiness or an ACK. Other
// validated domains are ignored, not reported replayed.
func ReplayOffline(ctx context.Context, m Maintenance, events []string) error {
	s := m.ReadStorage()
	if _, err := s.AssertSupported(ctx); err != nil {
		return err
	}
	heads := map[string]Receipt{}
	for _, raw := range events {
		if err := recovery.ValidateEvent(raw); err != nil {
			return err
		}
		v, _ := jsjson.Parse(raw)
		e := v.(*jsjson.Object)
		kind, _ := e.Get("kind")
		if kind != "external_cost_receipt" {
			continue
		}
		value, _ := e.Get("receipt")
		r, err := parseObject(value.(*jsjson.Object))
		if err != nil {
			return err
		}
		previous, exists := heads[r.ID()]
		if exists && equal(previous.value, r.value) {
			continue
		}
		revision := 0
		if exists {
			revision = previous.Revision()
		}
		if r.Revision() != revision+1 {
			return errors.New("recovery_external_cost_revision_gap")
		}
		heads[r.ID()] = r
		if err = m.ApplyReceipt(ctx, func(tx Storage) error { return tx.Put(ctx, r) }); err != nil {
			return err
		}
	}
	current, err := s.Current(ctx)
	if err != nil {
		return err
	}
	if len(current) != len(heads) {
		return errors.New("recovery_external_cost_head_mismatch")
	}
	for _, r := range current {
		h, ok := heads[r.ID()]
		if !ok || !equal(r.value, h.value) {
			return errors.New("recovery_external_cost_head_mismatch")
		}
	}
	return nil
}
