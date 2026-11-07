package recovery

import "database/sql"

// SessionRows expires with its session. Row-local errors do not poison unrelated
// statements, but native rollback is checked before every subsequent SQL step.
type SessionRows struct {
	owner  *WriteSession
	rows   *sql.Rows
	err    error
	closed bool
}

func (r *SessionRows) Next() bool {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.closed {
		return false
	}
	if e := r.owner.check(); e != nil {
		r.err = e
		_ = r.close()
		return false
	}
	next := r.rows.Next()
	if e := r.rows.Err(); e != nil {
		r.err = r.owner.after(e)
	}
	if !next {
		_ = r.close()
	}
	return next
}
func (r *SessionRows) Scan(dest ...any) error {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if e := r.owner.check(); e != nil {
		return e
	}
	return r.rows.Scan(dest...)
}
func (r *SessionRows) Columns() ([]string, error) {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if e := r.owner.check(); e != nil {
		return nil, e
	}
	return r.rows.Columns()
}
func (r *SessionRows) Err() error {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	return r.rows.Err()
}
func (r *SessionRows) Close() error { r.owner.mu.Lock(); defer r.owner.mu.Unlock(); return r.close() }
func (r *SessionRows) close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	delete(r.owner.rows, r)
	e := r.rows.Close()
	if r.err == nil {
		r.err = e
	}
	return e
}
