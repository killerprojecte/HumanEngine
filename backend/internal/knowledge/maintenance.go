package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// NewMaintenanceStore binds only a guarded, caller-owned offline staging scope.
// It never constructs or substitutes live write authority.
func NewMaintenanceStore(owner *recovery.Maintenance, tx *recovery.MaintenanceTx, ctx context.Context) (*Store, error) {
	if ctx == nil || owner == nil {
		return nil, errors.New("knowledge_maintenance_owner_required")
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
		n.bindingError = errors.New("knowledge_maintenance_owner_required")
		return &n
	}
	n.maintenanceTx, n.maintenanceContext = tx, ctx
	n.bindingError = tx.CheckBinding(ctx, s.maintenance)
	return &n
}
func (s *Store) query(q string, args ...any) (storageRows, error) {
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

type storageRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

func (s *Store) requireReader(minimum int) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if s.maintenance != nil {
		return s.maintenanceTx.RequireReader(s.maintenanceContext, minimum)
	}
	return s.session.RequireReader(minimum)
}
func (s *Store) ensureKnowledgeSchema() error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	if s.maintenance != nil {
		return s.maintenanceTx.EnsureKnowledgeStorageSchema(s.maintenanceContext)
	}
	return s.session.EnsureKnowledgeStorageSchema()
}

func (s *Service) WithMaintenanceTx(ctx context.Context, tx *recovery.MaintenanceTx) *Service {
	return &Service{Storage: s.Storage.WithMaintenanceTx(ctx, tx)}
}

// PurgeInMaintenanceAssertionScope borrows the exact assertion mutation child.
func (s *Service) PurgeInMaintenanceAssertionScope(store *assertions.Store, id string) error {
	if s == nil || s.Storage == nil || store == nil {
		return errors.New("knowledge_current_owner_required")
	}
	return store.WithMaintenanceScope(func(tx *recovery.MaintenanceTx) error {
		return s.WithMaintenanceTx(s.Storage.maintenanceContext, tx).PurgeAssertion(id)
	})
}
