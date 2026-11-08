package recovery

import (
	"context"
	"database/sql"
	"errors"
)

// MaintenanceStatement owns a real native prepared query. All execution and
// deferred stepping stays guarded by the admitting transaction's loss latch.
type MaintenanceStatement struct {
	owner  *MaintenanceTx
	stmt   *sql.Stmt
	ctx    context.Context
	closed bool
}

func (t *MaintenanceTx) PrepareQuery(ctx context.Context, query string) (*MaintenanceStatement, error) {
	t.m.sqlMu.Lock()
	defer t.m.sqlMu.Unlock()
	if e := t.usable(ctx); e != nil {
		return nil, e
	}
	s, e := t.m.conn.PrepareContext(ctx, query)
	if e != nil {
		return nil, t.fail(e)
	}
	p := &MaintenanceStatement{owner: t, stmt: s, ctx: ctx}
	t.statements = append(t.statements, p)
	return p, nil
}
func (p *MaintenanceStatement) Query(args ...any) (*MaintenanceRows, error) {
	t := p.owner
	t.m.sqlMu.Lock()
	defer t.m.sqlMu.Unlock()
	if e := t.usable(p.ctx); e != nil {
		return nil, e
	}
	if p.closed {
		return nil, sql.ErrTxDone
	}
	rows, e := p.stmt.QueryContext(p.ctx, args...)
	if e != nil {
		return nil, t.fail(e)
	}
	r := &MaintenanceRows{owner: t, rows: rows, ctx: p.ctx}
	t.rows = append(t.rows, r)
	return r, nil
}
func (p *MaintenanceStatement) closeLocked() error {
	if p.closed {
		return nil
	}
	p.closed = true
	return p.owner.fail(p.stmt.Close())
}
func (p *MaintenanceStatement) Close() error {
	p.owner.m.sqlMu.Lock()
	defer p.owner.m.sqlMu.Unlock()
	return p.closeLocked()
}

// RecordReplayEffect is separate from validation: every actual source revoke
// contributes once, including repeated identical revocations in traversal order.
func (t *MaintenanceTx) RecordReplayEffect(ctx context.Context, event Revocation) error {
	if e := t.CheckScope(ctx); e != nil {
		return e
	}
	raw, e := encodeRevocation(event)
	if e != nil {
		return e
	}
	if t.m.effects != nil {
		*t.m.effects = append(*t.m.effects, raw.payload)
	}
	return nil
}

// CaptureEffects returns receipts only after the whole offline transaction
// succeeds. It never changes the authority head or marks quarantine ready.
func (m *Maintenance) CaptureEffects(ctx context.Context, fn func(*MaintenanceTx) error) (effects []string, err error) {
	if err = m.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer m.mu.Unlock()
	active, e := transactionActive(m.conn)
	if e != nil {
		return nil, e
	}
	if active || m.effects != nil {
		return nil, errors.New("recovery_capture_transaction_active")
	}
	effects = []string{}
	m.effects = &effects
	defer func() {
		m.effects = nil
		if err != nil {
			effects = nil
		}
	}()
	m.failed = nil
	err = m.applyTransaction(ctx, 1, fn, true)
	return
}
func (t *MaintenanceTx) WorkingSetSchemaSupported(ctx context.Context) (bool, error) {
	t.m.sqlMu.Lock()
	defer t.m.sqlMu.Unlock()
	if e := t.usable(ctx); e != nil {
		return false, e
	}
	ok, e := workingSetSchemaSupported(ctx, t.m.conn)
	return ok, t.fail(e)
}
