package commitments

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sync/atomic"
)

// BindScope borrows the admitted offline owner's exact child. Domain operations
// reuse the real privacy service and expire with the enclosing maintenance Apply.
func (o *OfflineMaintenance) BindScope(ctx context.Context, tx *recovery.MaintenanceTx) (*PrivacyService, error) {
	if e := tx.CheckBinding(ctx, o.owner); e != nil {
		return nil, e
	}
	store := &Store{options: o.options, maintenance: &maintenanceScope{o.owner, tx, ctx}}
	return NewPrivacyService(store, o.owners)
}
func (s *Store) ReadMaintenanceCurrent(ctx context.Context, arStore *assertions.Store, enabled func(Context) (bool, error), options assertionauthority.Options, fn func(*ReadService) error) error {
	if s.maintenance == nil {
		return errors.New("commitment_maintenance_required")
	}
	if e := s.requireTransaction(); e != nil {
		return e
	}
	return arStore.WithMaintenanceScope(func(tx *recovery.MaintenanceTx) error {
		if tx != s.maintenance.tx {
			return errors.New("commitment_maintenance_binding_mismatch")
		}
		return arStore.Read(ctx, func(ar *assertions.Reader) error {
			return ar.WithSupportReadStatements(func(reader *assertions.Reader) error {
				service, e := newReadService(s.reader(), reader, enabled, options)
				if e != nil {
					return e
				}
				var active atomic.Bool
				active.Store(true)
				defer active.Store(false)
				check := service.check
				service.check = func() error {
					if !active.Load() {
						return sql.ErrTxDone
					}
					return check()
				}
				return errors.Join(fn(service), check())
			})
		})
	})
}
func (s *Store) WithMaintenanceTx(ctx context.Context, tx *recovery.MaintenanceTx) *Store {
	if s.maintenance == nil {
		n := *s
		n.bindingError = errors.New("commitment_maintenance_required")
		return &n
	}
	return s.withMaintenanceChild(ctx, tx)
}
func (s *PrivacyService) WithMaintenanceTx(ctx context.Context, tx *recovery.MaintenanceTx) *PrivacyService {
	n := *s
	n.Store = s.Store.WithMaintenanceTx(ctx, tx)
	return &n
}
