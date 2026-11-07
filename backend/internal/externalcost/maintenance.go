package externalcost

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// OfflineMaintenance adapts an independently admitted recovery capability to
// the external-cost domain. It never enrolls, acknowledges or marks main ready.
// The caller owns the staging connection exclusively. Storage and callback SQL
// are borrowed: do not retain them beyond the capability/callback lifetime.
type OfflineMaintenance struct{ capability *recovery.Maintenance }

func NewOfflineMaintenance(capability *recovery.Maintenance) *OfflineMaintenance {
	return &OfflineMaintenance{capability: capability}
}
func (m *OfflineMaintenance) ReadStorage() Storage {
	return Storage{DB: m.capability.Connection()}
}
func (m *OfflineMaintenance) ApplyReceipt(ctx context.Context, fn func(Storage) error) error {
	return m.capability.Apply(ctx, 10, func(tx *recovery.MaintenanceTx) error {
		return fn(Storage{DB: tx})
	})
}

var _ Maintenance = (*OfflineMaintenance)(nil)
