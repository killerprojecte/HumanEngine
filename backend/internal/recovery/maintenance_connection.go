package recovery

import (
	"context"
	"errors"
)

// PrepareReconciliationConnection applies Store's connection-local SQLite
// setup to the exclusively owned staging connection. In particular, recovered
// terminal deletes must execute the source foreign-key cascades. This changes
// neither an authority intent nor the quarantine gate.
func (m *Maintenance) PrepareReconciliationConnection(ctx context.Context) error {
	if e := m.mu.LockContext(ctx); e != nil {
		return e
	}
	defer m.mu.Unlock()
	active, e := transactionActive(m.conn)
	if e != nil {
		return e
	}
	if active {
		return errors.New("recovery_reconcile_transaction_active")
	}
	if _, e := m.conn.ExecContext(ctx, "PRAGMA journal_mode=WAL"); e != nil {
		return e
	}
	if _, e = m.conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); e != nil {
		return e
	}
	var enabled bool
	if e = m.conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); e != nil {
		return e
	}
	if !enabled {
		return errors.New("recovery_foreign_keys_required")
	}
	return nil
}

// RequireAutocommit guards an offline filesystem phase. Pure SQL domain owners
// continue to support nested scopes; callers of this method exclusively own the
// staging connection and must not start a transaction until file work finishes.
func (m *Maintenance) RequireAutocommit(ctx context.Context) error {
	if e := m.mu.LockContext(ctx); e != nil {
		return e
	}
	defer m.mu.Unlock()
	active, e := transactionActive(m.conn)
	if e != nil {
		return e
	}
	if active {
		return errors.New("recovery_reconcile_transaction_active")
	}
	return nil
}
