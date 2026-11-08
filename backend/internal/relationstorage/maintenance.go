package relationstorage

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// NewMaintenanceStore binds only a guarded, caller-owned offline staging scope.
// It never constructs or substitutes live write authority.
func NewMaintenanceStore(owner *recovery.Maintenance, tx *recovery.MaintenanceTx, ctx context.Context) (*Store, error) {
	if ctx == nil || owner == nil {
		return nil, errors.New("relation_maintenance_owner_required")
	}
	s := (&Store{maintenance: owner}).WithMaintenanceTx(ctx, tx)
	if e := s.RequireTransaction(); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *Store) WithMaintenanceTx(ctx context.Context, tx *recovery.MaintenanceTx) *Store {
	n := *s
	if s.bindingError != nil {
		return &n
	}
	if s.maintenance == nil || s.session != nil || ctx == nil {
		n.bindingError = errors.New("relation_maintenance_owner_required")
		return &n
	}
	n.maintenanceTx, n.maintenanceContext = tx, ctx
	n.bindingError = tx.CheckBinding(ctx, s.maintenance)
	return &n
}
func (s *Store) query(q string, args ...any) (Rows, error) {
	if e := s.RequireTransaction(); e != nil {
		return nil, e
	}
	if s.maintenance != nil {
		return s.maintenanceTx.QueryContext(s.maintenanceContext, q, args...)
	}
	return s.session.Query(q, args...)
}
func (s *Store) execSQL(q string, args ...any) (sql.Result, error) {
	if e := s.RequireTransaction(); e != nil {
		return nil, e
	}
	if s.maintenance != nil {
		return s.maintenanceTx.ExecContext(s.maintenanceContext, q, args...)
	}
	return s.session.Exec(q, args...)
}
