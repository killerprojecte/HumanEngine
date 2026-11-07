package commitments

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// This binding is created only inside the admitted owner's Apply callback.
// It never converts maintenance quarantine into a live WriteSession.
type maintenanceScope struct {
	owner *recovery.Maintenance
	tx    *recovery.MaintenanceTx
	ctx   context.Context
}

func (s *maintenanceScope) check() error { return s.tx.CheckBinding(s.ctx, s.owner) }
func (s *maintenanceScope) requireReaders(temporal bool) error {
	if e := s.check(); e != nil {
		return e
	}
	// Apply's existing guarded reader update is the effect. Empty domain callbacks
	// do not suppress required work: there is deliberately no new replay intent.
	upgrade := func(minimum int) error {
		return s.tx.Apply(s.ctx, minimum, func(*recovery.MaintenanceTx) error { return nil })
	}
	if e := upgrade(3); e != nil {
		return e
	}
	if temporal {
		return upgrade(10)
	}
	return nil
}
func (s *Store) withMaintenanceChild(ctx context.Context, child *recovery.MaintenanceTx) *Store {
	n := *s
	n.maintenance = &maintenanceScope{s.maintenance.owner, child, ctx}
	n.bindingError = n.maintenance.check()
	return &n
}
func (s *Store) ensureTemporalSchema() error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if s.maintenance != nil {
		return s.maintenance.tx.EnsureCommitmentTemporalSchema(s.maintenance.ctx)
	}
	return s.session.EnsureCommitmentTemporalSchema()
}

// WithMaintenanceScope lets required offline domain owners bind to the same
// guarded child. It exposes no connection and does not accept a replacement
// owner. The handle and derived adapters expire when Apply's callback returns.
func (s *Store) WithMaintenanceScope(fn func(*recovery.MaintenanceTx) error) error {
	if s.maintenance == nil {
		return errors.New("commitment_maintenance_required")
	}
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if fn == nil {
		return errors.New("commitment_write_operation_required")
	}
	e := fn(s.maintenance.tx)
	return errors.Join(e, s.requireTransaction())
}
