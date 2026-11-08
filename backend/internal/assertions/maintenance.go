package assertions

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type maintenanceScope struct {
	owner *recovery.Maintenance
	tx    *recovery.MaintenanceTx
	ctx   context.Context
}

// NewMaintenanceStore is an explicit quarantined domain adapter. It cannot be
// converted to a live writer and never publishes or acquires a second connection.
func NewMaintenanceStore(owner *recovery.Maintenance, tx *recovery.MaintenanceTx, ctx context.Context, options Options) (*Store, error) {
	if ctx == nil || owner == nil || tx == nil || options.DateParser == nil {
		return nil, errors.New("assertion_maintenance_required")
	}
	if e := tx.CheckBinding(ctx, owner); e != nil {
		return nil, e
	}
	return &Store{maintenance: &maintenanceScope{owner, tx, ctx}, options: options}, nil
}
func (s *Store) withMaintenanceChild(ctx context.Context, tx *recovery.MaintenanceTx) *Store {
	n := *s
	n.maintenance = &maintenanceScope{s.maintenance.owner, tx, ctx}
	n.bindingError = tx.CheckBinding(ctx, n.maintenance.owner)
	return &n
}
func (s *Store) WithMaintenanceScope(fn func(*recovery.MaintenanceTx) error) error {
	if s.maintenance == nil {
		return errors.New("assertion_maintenance_required")
	}
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if fn == nil {
		return errors.New("assertion_write_operation_required")
	}
	return errors.Join(fn(s.maintenance.tx), s.requireTransaction())
}
func (s *Store) appendIntent(event recovery.Revocation) (recovery.Head, error) {
	if e := s.requireTransaction(); e != nil {
		return recovery.Head{}, e
	}
	if s.maintenance != nil {
		return recovery.Head{}, s.maintenance.tx.RecordReplayEffect(s.maintenance.ctx, event)
	}
	return s.session.AppendIntent(event)
}
func (s *Store) requireReader(minimum int) error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if s.maintenance != nil {
		return s.maintenance.tx.Apply(s.maintenance.ctx, minimum, func(*recovery.MaintenanceTx) error { return nil })
	}
	return s.session.RequireReader(minimum)
}

type maintenancePreparedRead struct {
	statement *recovery.MaintenanceStatement
}

func (p *maintenancePreparedRead) Query(args ...any) (Rows, error) {
	return p.statement.Query(bind(args)...)
}
func (p *maintenancePreparedRead) Close() error { return p.statement.Close() }
func (s *Store) WithMaintenanceTx(ctx context.Context, tx *recovery.MaintenanceTx) *Store {
	if s.maintenance == nil {
		n := *s
		n.bindingError = errors.New("assertion_maintenance_required")
		return &n
	}
	return s.withMaintenanceChild(ctx, tx)
}
