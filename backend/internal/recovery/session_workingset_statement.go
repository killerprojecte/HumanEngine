package recovery

import (
	"database/sql"
	"errors"
)

// WorkingSetReadStatement owns a real compiled SQLite query, never its results.
// The publication guard is active during preparation and every execution.
type WorkingSetReadStatement struct {
	owner     *WorkingSetReadSession
	statement *sql.Stmt
	closed    bool
}

func (r *WorkingSetReadSession) PrepareQuery(query string) (*WorkingSetReadStatement, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.check(); e != nil {
		return nil, e
	}
	statement, e := r.conn.PrepareContext(r.ctx, query)
	if e != nil {
		return nil, errors.Join(e, r.checkNative())
	}
	result := &WorkingSetReadStatement{owner: r, statement: statement}
	r.statements[result] = true
	return result, nil
}
func (s *WorkingSetReadStatement) Query(args ...any) (*WorkingSetReadRows, error) {
	r := s.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.check(); e != nil {
		return nil, e
	}
	if s.closed {
		return nil, sql.ErrTxDone
	}
	rows, e := s.statement.QueryContext(r.ctx, args...)
	if e != nil {
		return nil, errors.Join(e, r.checkNative())
	}
	result := &WorkingSetReadRows{owner: r, rows: rows}
	r.rows[result] = true
	return result, nil
}
func (s *WorkingSetReadStatement) closeLocked() error {
	if s.closed {
		return nil
	}
	s.closed = true
	delete(s.owner.statements, s)
	e := s.statement.Close()
	if e != nil {
		return errors.Join(e, s.owner.checkNative())
	}
	return nil
}
func (s *WorkingSetReadStatement) Close() error {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	return s.closeLocked()
}
