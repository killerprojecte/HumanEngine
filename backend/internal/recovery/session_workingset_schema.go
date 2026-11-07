package recovery

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

const workingSetDDL = `CREATE TABLE memory_working_set_pins(scene_id TEXT NOT NULL,kind TEXT NOT NULL,entity_id TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(scene_id,kind,entity_id));
CREATE TABLE memory_working_set_items(scene_id TEXT NOT NULL,kind TEXT NOT NULL,entity_id TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(scene_id,kind,entity_id));
CREATE TABLE memory_working_set_jobs(scene_id TEXT NOT NULL,kind TEXT NOT NULL,entity_id TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(scene_id,kind,entity_id));
CREATE TABLE memory_working_set_receipts(receipt_key TEXT PRIMARY KEY,selected_at INTEGER NOT NULL,value TEXT NOT NULL);`

// WorkingSetSchemaSupported audits the source's exact live optional-table
// contract. Fixed PRAGMAs are kept inside the capability; no arbitrary caller
// statement runs with schema privileges.
func (s *WriteSession) WorkingSetSchemaSupported() (exists bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(); err != nil {
		return
	}
	if err = s.protect(2); err != nil {
		return
	}
	defer func() { err = errors.Join(err, s.protect(0), s.check()) }()
	return workingSetSchemaSupported(s.ctx, s.conn)
}

func workingSetSchemaSupported(ctx context.Context, conn *sql.Conn) (exists bool, err error) {
	rows, err := conn.QueryContext(ctx, "SELECT name,type FROM sqlite_master WHERE name LIKE 'memory_working_set_%' AND type IN ('table','view')")
	if err != nil {
		return false, err
	}
	names := []string{}
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			break
		}
		if kind != "table" {
			err = errors.New("working_set_schema_unsupported")
			break
		}
		names = append(names, name)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return false, err
	}
	if len(names) == 0 {
		return false, nil
	}
	if len(names) != 4 {
		return false, errors.New("working_set_schema_unsupported")
	}
	expected := map[string][]string{"memory_working_set_pins": {"scene_id", "kind", "entity_id", "value"}, "memory_working_set_items": {"scene_id", "kind", "entity_id", "value"}, "memory_working_set_jobs": {"scene_id", "kind", "entity_id", "value"}, "memory_working_set_receipts": {"receipt_key", "selected_at", "value"}}
	for _, name := range names {
		columns, ok := expected[name]
		if !ok {
			return false, errors.New("working_set_schema_unsupported")
		}
		rs, e := conn.QueryContext(ctx, "PRAGMA main.table_info("+name+")")
		if e != nil {
			return false, e
		}
		i := 0
		for rs.Next() {
			var cid, notnull, pk int
			var col, typ string
			var dflt any
			if e = rs.Scan(&cid, &col, &typ, &notnull, &dflt, &pk); e != nil {
				break
			}
			if i >= len(columns) || columns[i] != col {
				e = errors.New("working_set_schema_unsupported")
				break
			}
			i++
		}
		e = errors.Join(e, rs.Err(), rs.Close())
		if e != nil {
			return false, e
		}
		if i != len(columns) {
			return false, errors.New("working_set_schema_unsupported")
		}
	}
	return true, nil
}
func (s *WriteSession) EnsureWorkingSetSchema() error {
	exists, e := s.WorkingSetSchemaSupported()
	if e != nil || exists {
		return e
	}
	return s.WithSavepoint(func(child *WriteSession) error { return child.schema(workingSetDDL) })
}

// WorkingSetReadSession is an opaque, synchronous publication capability. It
// never yields a raw connection or schema-control callback to its consumer.
type WorkingSetReadSession struct {
	mu         sync.Mutex
	active     bool
	ctx        context.Context
	conn       *sql.Conn
	rows       map[*WorkingSetReadRows]bool
	statements map[*WorkingSetReadStatement]bool
	fatal      error
}

func (r *WorkingSetReadSession) check() error {
	if !r.active {
		return sql.ErrTxDone
	}
	if r.fatal != nil {
		return r.fatal
	}
	if e := r.ctx.Err(); e != nil {
		r.fatal = e
		return e
	}
	return nil
}

// checkNative runs only at an error boundary, fixed audit exit or publication
// exit. Successful row iteration only checks lifetime, fatal state and context.
func (r *WorkingSetReadSession) checkNative() error {
	if e := r.check(); e != nil {
		return e
	}
	active, e := transactionActive(r.conn)
	if e != nil {
		r.fatal = e
	} else if !active {
		r.fatal = errors.Join(sql.ErrTxDone, errors.New("recovery_main_transaction_lost"))
	}
	return r.fatal
}
func (r *WorkingSetReadSession) CheckScope() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.check()
}
func (r *WorkingSetReadSession) Query(query string, args ...any) (*WorkingSetReadRows, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.check(); e != nil {
		return nil, e
	}
	rows, e := r.conn.QueryContext(r.ctx, query, args...)
	if e != nil {
		return nil, errors.Join(e, r.checkNative())
	}
	result := &WorkingSetReadRows{owner: r, rows: rows}
	r.rows[result] = true
	return result, nil
}

// WorkingSetReadRows never exposes sql.Rows. Delayed SQLite stepping errors
// report to the owner even if the caller omits Err or catches the returned error.
type WorkingSetReadRows struct {
	owner  *WorkingSetReadSession
	rows   *sql.Rows
	err    error
	closed bool
}

func (r *WorkingSetReadRows) after(e error) error {
	if e != nil {
		e = errors.Join(e, r.owner.checkNative())
		r.err = errors.Join(r.err, e)
	}
	return e
}
func (r *WorkingSetReadRows) Next() bool {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if e := r.owner.check(); e != nil {
		r.err = errors.Join(r.err, e)
		return false
	}
	if r.closed {
		return false
	}
	next := r.rows.Next()
	if !next {
		r.after(r.rows.Err())
	}
	return next
}
func (r *WorkingSetReadRows) Scan(dest ...any) error {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if e := r.owner.check(); e != nil {
		return e
	}
	return r.after(r.rows.Scan(dest...))
}
func (r *WorkingSetReadRows) Err() error {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	r.after(r.rows.Err())
	return errors.Join(r.err, r.owner.check())
}
func (r *WorkingSetReadRows) closeLocked() error {
	if r.closed {
		return nil
	}
	r.closed = true
	delete(r.owner.rows, r)
	e := r.rows.Close()
	return errors.Join(r.after(e), r.after(r.rows.Err()))
}
func (r *WorkingSetReadRows) Close() error {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	return r.closeLocked()
}

func (r *WorkingSetReadSession) SchemaSupported() (exists bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err = r.check(); err != nil {
		return
	}
	if err = protectMutation(r.conn, false); err != nil {
		return
	}
	defer func() { err = errors.Join(err, protectPreparation(r.conn), r.checkNative()) }()
	return workingSetSchemaSupported(r.ctx, r.conn)
}

// WithWorkingSetRead shares Publish's authority-verified main WAL snapshot,
// without taking a main writer reservation. Fixed audit PRAGMAs alone run in a
// private privilege window; no caller code executes during that window.
func (a *Authority) WithWorkingSetRead(ctx context.Context, main *sql.DB, fn func(*WorkingSetReadSession) error) error {
	if fn == nil {
		return errors.New("working_set_callback_required")
	}
	return a.Publish(ctx, main, func(conn *sql.Conn) (out []byte, err error) {
		if err = protectPreparation(conn); err != nil {
			return nil, err
		}
		r := &WorkingSetReadSession{active: true, ctx: ctx, conn: conn, rows: map[*WorkingSetReadRows]bool{}, statements: map[*WorkingSetReadStatement]bool{}}
		defer func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			err = errors.Join(err, r.checkNative())
			for rows := range r.rows {
				err = errors.Join(err, rows.closeLocked())
			}
			for statement := range r.statements {
				err = errors.Join(err, statement.closeLocked())
			}
			err = errors.Join(err, r.checkNative())
			r.active = false
			err = errors.Join(err, protectMutation(conn, false))
		}()
		return nil, fn(r)
	}, func([]byte) error { return nil })
}
