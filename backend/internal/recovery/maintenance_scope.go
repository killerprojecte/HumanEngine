package recovery

import (
	"context"
	"errors"
)

// CheckScope checks the existing guarded maintenance capability without issuing
// SQL, upgrading compatibility, or exposing the caller-owned connection. Native
// transaction loss is latched by usable exactly as for statement operations.
func (t *MaintenanceTx) CheckScope(ctx context.Context) error {
	if t == nil || t.m == nil {
		return errors.New("recovery_maintenance_denied")
	}
	t.m.sqlMu.Lock()
	defer t.m.sqlMu.Unlock()
	return t.usable(ctx)
}

// CheckBinding additionally requires the same admitted staging owner. A domain
// adapter cannot attach an unrelated MaintenanceTx to an existing owner.
func (t *MaintenanceTx) CheckBinding(ctx context.Context, owner *Maintenance) error {
	if e := t.CheckScope(ctx); e != nil {
		return e
	}
	if owner == nil || t.m != owner {
		return errors.New("recovery_maintenance_binding_mismatch")
	}
	return nil
}
