package recovery

import (
	"database/sql"
	"errors"
)

// SessionStatement is an opaque prepared-query capability on its owner's main
// connection. It never exposes sql.Stmt or extends its owner's lifetime. As with
// Query, caller SQL remains subject to the session authorizer; preparing a query
// does not grant schema, gate, transaction-control or attached-database access.
// Use it synchronously within the owning callback, not concurrently with return.
// An ancestor's statements are suspended while a child savepoint is active.
type SessionStatement struct {
	owner  *WriteSession
	stmt   *sql.Stmt
	closed bool
	query  string
	rows   map[*SessionRows]struct{}
}

// PrepareQuery prepares lazily at the caller's read site, without taking an
// authority lock, changing the authorizer or invoking a user callback. Close
// releases the handle early; otherwise the owning session closes it on exit.
func (s *WriteSession) PrepareQuery(query string) (*SessionStatement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	stmt, err := s.conn.PrepareContext(s.ctx, query)
	if err = s.after(err); err != nil {
		if stmt != nil {
			err = errors.Join(err, stmt.Close())
		}
		return nil, err
	}
	h := &SessionStatement{owner: s, stmt: stmt, query: query, rows: make(map[*SessionRows]struct{})}
	if s.statements == nil {
		s.statements = make(map[*SessionStatement]struct{})
	}
	s.statements[h] = struct{}{}
	return h, nil
}

func (h *SessionStatement) Query(args ...any) (*SessionRows, error) {
	s := h.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	if h.closed {
		return nil, sql.ErrTxDone
	}
	if e := s.captureRead(h.query, args); e != nil {
		return nil, e
	}
	r, err := h.stmt.QueryContext(s.ctx, args...)
	if err != nil {
		return nil, s.after(err)
	}
	rows := &SessionRows{owner: s, rows: r, statement: h}
	if s.rows == nil {
		s.rows = make(map[*SessionRows]struct{})
	}
	s.rows[rows] = struct{}{}
	h.rows[rows] = struct{}{}
	return rows, nil
}

func (h *SessionStatement) Close() error {
	h.owner.mu.Lock()
	defer h.owner.mu.Unlock()
	return h.close()
}

func (h *SessionStatement) close() (err error) {
	if h.closed {
		return nil
	}
	h.closed = true
	for r := range h.rows {
		err = errors.Join(err, r.close())
	}
	delete(h.owner.statements, h)
	return errors.Join(err, h.stmt.Close())
}
