package recovery

import (
	"context"
	"database/sql"
)

// MaintenanceRows controls deferred SQLite stepping and expires with its
// callback. No native rows escape to a domain repository.
type MaintenanceRows struct {
	owner  *MaintenanceTx
	rows   *sql.Rows
	ctx    context.Context
	err    error
	closed bool
}

func (r *MaintenanceRows) fail(err error) error {
	if err != nil && r.err == nil {
		r.err = err
	}
	if r.owner.active {
		return r.owner.fail(err)
	}
	return err
}
func (r *MaintenanceRows) Next() bool {
	r.owner.m.sqlMu.Lock()
	defer r.owner.m.sqlMu.Unlock()
	if r.closed {
		return false
	}
	if e := r.owner.usable(r.ctx); e != nil {
		r.fail(e)
		r.closeLocked()
		return false
	}
	next := r.rows.Next()
	r.fail(r.rows.Err())
	return next
}
func (r *MaintenanceRows) Scan(dest ...any) error {
	r.owner.m.sqlMu.Lock()
	defer r.owner.m.sqlMu.Unlock()
	if e := r.owner.usable(r.ctx); e != nil {
		return r.fail(e)
	}
	return r.fail(r.rows.Scan(dest...))
}
func (r *MaintenanceRows) Err() error {
	r.owner.m.sqlMu.Lock()
	defer r.owner.m.sqlMu.Unlock()
	r.fail(r.rows.Err())
	return r.err
}
func (r *MaintenanceRows) Close() error {
	r.owner.m.sqlMu.Lock()
	defer r.owner.m.sqlMu.Unlock()
	return r.closeLocked()
}
func (r *MaintenanceRows) closeLocked() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.fail(r.rows.Err())
	return r.fail(r.rows.Close())
}

// MaintenanceRow defers execution until Scan so an escaped Row cannot execute
// outside its callback. No-rows is an ordinary repository lookup result.
type MaintenanceRow struct {
	owner *MaintenanceTx
	ctx   context.Context
	query string
	args  []any
	used  bool
}

func (r *MaintenanceRow) Scan(dest ...any) error {
	r.owner.m.sqlMu.Lock()
	if r.used {
		r.owner.m.sqlMu.Unlock()
		return sql.ErrNoRows
	}
	r.used = true
	r.owner.m.sqlMu.Unlock()
	rows, err := r.owner.QueryContext(r.ctx, r.query, r.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	return rows.Scan(dest...)
}
