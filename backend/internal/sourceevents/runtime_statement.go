package sourceevents

import (
	"database/sql"
	"errors"
)

// RuntimeReadStatement is a native, callback-leased assertion read handle.
// It cannot retain a connection, execute writes, or survive scope return.
type RuntimeReadStatement interface {
	Query(...any) (Rows, error)
	Close() error
}
type nativeRuntimeStatement struct {
	query func(...any) (Rows, error)
	close func() error
}

func (s *nativeRuntimeStatement) Query(args ...any) (Rows, error) { return s.query(args...) }
func (s *nativeRuntimeStatement) Close() error                    { return s.close() }

type scopedRuntimeStatement struct {
	statement RuntimeReadStatement
	ready     func() error
	closed    bool
	rows      map[*runtimeStatementRows]struct{}
}

func (s *scopedRuntimeStatement) Query(args ...any) (Rows, error) {
	if e := s.ready(); e != nil {
		return nil, e
	}
	if s.closed {
		return nil, sql.ErrTxDone
	}
	rows, e := s.statement.Query(args...)
	if e != nil {
		return nil, e
	}
	wrapped := &runtimeStatementRows{owner: s, scopedRuntimeRows: &scopedRuntimeRows{Rows: rows, ready: func() error {
		if e := s.ready(); e != nil {
			return e
		}
		if s.closed {
			return sql.ErrTxDone
		}
		return nil
	}}}
	if s.rows == nil {
		s.rows = map[*runtimeStatementRows]struct{}{}
	}
	s.rows[wrapped] = struct{}{}
	return wrapped, nil
}
func (s *scopedRuntimeStatement) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	for rows := range s.rows {
		err = errors.Join(err, rows.Close())
	}
	return errors.Join(err, s.statement.Close())
}

type runtimeStatementRows struct {
	*scopedRuntimeRows
	owner *scopedRuntimeStatement
}

func (r *runtimeStatementRows) Close() error {
	delete(r.owner.rows, r)
	return r.scopedRuntimeRows.Close()
}
