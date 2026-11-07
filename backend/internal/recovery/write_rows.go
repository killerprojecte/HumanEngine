package recovery

import "database/sql"

// MainRows never exposes its underlying sql.Rows. Advancing a SQLite statement
// can fail after Query returns, including an automatic transaction rollback.
// Serialize that work with Exec/Query and latch errors immediately, rather than
// allowing another statement to slip between a native-state check and rollback.
// These rows also expire with their preparation/mutation capability.
type MainRows struct {
	owner   *MainMutation
	rows    *sql.Rows
	err     error
	closed  bool
	started bool
}

func (r *MainRows) failLocked(err error) error {
	if err != nil && r.err == nil {
		r.err = err
	}
	r.owner.failLocked(err)
	return err
}
func (r *MainRows) Next() bool {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.closed {
		return false
	}
	// Check native state at statement entry; later steps share the same lock
	// and every intervening statement/error is tracked by the owning capability.
	var err error
	if r.started {
		err = r.owner.usableLocked()
	} else {
		err = r.owner.transactionLocked()
	}
	if err != nil {
		r.failLocked(err)
		r.closeLocked()
		return false
	}
	r.started = true
	next := r.rows.Next()
	r.failLocked(r.rows.Err())
	return next
}
func (r *MainRows) NextResultSet() bool {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.closed {
		return false
	}
	if err := r.owner.transactionLocked(); err != nil {
		r.failLocked(err)
		r.closeLocked()
		return false
	}
	next := r.rows.NextResultSet()
	r.failLocked(r.rows.Err())
	r.started = false
	return next
}
func (r *MainRows) Scan(dest ...any) error {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if err := r.owner.usableLocked(); err != nil {
		return r.failLocked(err)
	}
	return r.failLocked(r.rows.Scan(dest...))
}
func (r *MainRows) Columns() ([]string, error) {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if err := r.owner.usableLocked(); err != nil {
		return nil, r.failLocked(err)
	}
	columns, err := r.rows.Columns()
	return columns, r.failLocked(err)
}
func (r *MainRows) ColumnTypes() ([]*sql.ColumnType, error) {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if err := r.owner.usableLocked(); err != nil {
		return nil, r.failLocked(err)
	}
	columns, err := r.rows.ColumnTypes()
	return columns, r.failLocked(err)
}
func (r *MainRows) Err() error {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	r.failLocked(r.rows.Err())
	return r.err
}
func (r *MainRows) Close() error {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	return r.closeLocked()
}
func (r *MainRows) closeLocked() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.failLocked(r.rows.Err())
	return r.failLocked(r.rows.Close())
}
