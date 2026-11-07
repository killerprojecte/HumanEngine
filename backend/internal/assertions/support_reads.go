package assertions

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const (
	supportRecordSQL = "SELECT value FROM memories WHERE id=?"
	supportProofSQL  = "SELECT value FROM memory_supports WHERE memory_id=?"
)

// Only compiled statements are retained, never a row, decoded JSON or an
// authority decision. These are the source repository's two fixed operations,
// not a resource/admission cap.
type preparedRead interface {
	Query(...any) (Rows, error)
	Close() error
}
type supportReadHandle struct {
	statement preparedRead
	executing bool
	retired   bool
}
type supportReadScope struct {
	handles map[string]*supportReadHandle
}

// WithSupportReadStatements runs one synchronous refresh with independent,
// lazily prepared Get/Supports statements. Use the supplied reader in fn. Nested
// refreshes own separate handles; returning restores use of the outer reader.
// Every call still reads SQL and parses a fresh value, and Get still checks
// readiness before reading. Scope exit, including panic, expires the supplied
// reader and closes only its statements, never the borrowed main connection.
// Like the source synchronous repository, do not use a reader concurrently or
// after its callback returns. Write-session ancestor suspension still applies.
func (r *Reader) WithSupportReadStatements(fn func(*Reader) error) (err error) {
	if err = r.check(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("assertion_read_operation_required")
	}
	var active atomic.Bool
	active.Store(true)
	check := func() error {
		if !active.Load() {
			return sql.ErrTxDone
		}
		return r.check()
	}
	child := *r
	child.supportReads = &supportReadScope{handles: make(map[string]*supportReadHandle)}
	child.check = check
	child.ready = func() error {
		if e := check(); e != nil {
			return e
		}
		return r.ready()
	}
	child.query = func(query string, args ...any) (Rows, error) {
		if e := check(); e != nil {
			return nil, e
		}
		return r.query(query, args...)
	}
	child.prepare = func(query string) (preparedRead, error) {
		if e := check(); e != nil {
			return nil, e
		}
		return r.prepare(query)
	}
	defer func() {
		active.Store(false)
		err = errors.Join(err, child.supportReads.clear())
	}()
	return fn(&child)
}

func (scope *supportReadScope) clear() (err error) {
	for key, handle := range scope.handles {
		delete(scope.handles, key)
		handle.retired = true
		if !handle.executing {
			err = errors.Join(err, handle.statement.Close())
		}
	}
	return err
}

func (r *Reader) supportOne(query, id string, dest ...any) (found bool, err error) {
	if r.supportReads == nil {
		return r.one(query, []any{id}, dest...)
	}
	if err = r.check(); err != nil {
		return false, err
	}
	scope := r.supportReads
	handle := scope.handles[query]
	temporary := handle != nil && handle.executing
	// Reentry takes the original prepare path instead of using an executing
	// statement. Any native busy-connection failure remains a SQL failure.
	if handle == nil || temporary {
		statement, e := r.prepare(query)
		if e != nil {
			return false, errors.Join(e, scope.clear())
		}
		handle = &supportReadHandle{statement: statement}
		if !temporary {
			scope.handles[query] = handle
		}
	}
	handle.executing = true
	defer func() {
		handle.executing = false
		if temporary || handle.retired {
			err = errors.Join(err, handle.statement.Close())
		}
		// Read/step/scan/close failures invalidate both handles. JSON parse and
		// readiness failures happen outside this function and do not evict.
		if err != nil {
			err = errors.Join(err, scope.clear())
		}
	}()
	rows, err := handle.statement.Query(id)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if rows.Next() {
		return true, rows.Scan(dest...)
	}
	return false, rows.Err()
}

// The two adapters keep binding and lifetime checks private. Neither exposes
// sql.Stmt, sql.Conn, or an alternate authority/database connection.
type sessionPreparedRead struct{ statement *recovery.SessionStatement }

func (p *sessionPreparedRead) Query(args ...any) (Rows, error) {
	return p.statement.Query(bind(args)...)
}
func (p *sessionPreparedRead) Close() error { return p.statement.Close() }

type publicationPreparedRead struct {
	statement *sql.Stmt
	ctx       context.Context
	check     func() error
}

func (p *publicationPreparedRead) Query(args ...any) (Rows, error) {
	if e := p.check(); e != nil {
		return nil, e
	}
	return p.statement.QueryContext(p.ctx, bind(args)...)
}
func (p *publicationPreparedRead) Close() error { return p.statement.Close() }
