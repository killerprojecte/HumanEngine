package recovery

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/mattn/go-sqlite3"
)

// WriteSession composes synchronous trusted stores in one main transaction.
// Unlike MainMutation, ordinary statement errors are recoverable by the caller.
// Native transaction loss and cancellation permanently expire its SQL capability.
// Do not use it concurrently with its owner callback's return, or reenter the
// Authority through Write/Publish: use this session for nested store operations.
type WriteSession struct {
	*writeSessionState
	active bool
	rows   map[*SessionRows]struct{}
}

// All nested capabilities share the root lock, context and fatal native state.
// Only the innermost callback may use its capability until it returns.
type writeSessionState struct {
	mu        sync.Mutex
	authority *Authority
	main      *sql.DB
	conn      *sql.Conn
	ctx       context.Context
	fatal     error
	current   *WriteSession
}

func (a *Authority) WithWriteSession(ctx context.Context, main *sql.DB, fn func(*WriteSession) error) error {
	return a.withWriteSession(ctx, main, fn, nil)
}
func (a *Authority) withWriteSession(ctx context.Context, main *sql.DB, fn func(*WriteSession) error, commit commitExec) (err error) {
	if fn == nil {
		return errors.New("recovery_write_invalid")
	}
	conn, err := main.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = writableMain(ctx, conn, a.path); err != nil {
		return err
	}
	if err = beginOwned(ctx, conn, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollbackOwned(conn)) }()
	if _, err = conn.ExecContext(ctx, "UPDATE main.recovery_gate SET id=id WHERE 0"); err != nil {
		return err
	}
	s := &WriteSession{writeSessionState: &writeSessionState{authority: a, main: main, conn: conn, ctx: ctx}, active: true}
	s.current = s
	if err = s.protect(0); err != nil {
		return err
	}
	func() { defer func() { err = errors.Join(err, s.finish()) }(); err = fn(s) }()
	if err != nil {
		return err
	}
	return commitOwned(ctx, conn, "main", commit)
}
func (s *WriteSession) check() error {
	if !s.active {
		return sql.ErrTxDone
	}
	if s.fatal != nil {
		return s.fatal
	}
	if s.current != s {
		return errors.New("recovery_session_scope_inactive")
	}
	if err := s.ctx.Err(); err != nil {
		s.fatal = err
		return err
	}
	active, err := transactionActive(s.conn)
	if err != nil {
		s.fatal = err
	} else if !active {
		s.fatal = errors.Join(sql.ErrTxDone, errors.New("recovery_main_transaction_lost"))
	}
	return s.fatal
}
func (s *WriteSession) after(err error) error { return errors.Join(err, s.check()) }
func (s *WriteSession) Exec(query string, args ...any) (sql.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	r, e := s.conn.ExecContext(s.ctx, query, args...)
	return r, s.after(e)
}
func (s *WriteSession) Query(query string, args ...any) (*SessionRows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	r, e := s.conn.QueryContext(s.ctx, query, args...)
	if e != nil {
		return nil, s.after(e)
	}
	rows := &SessionRows{owner: s, rows: r}
	if s.rows == nil {
		s.rows = make(map[*SessionRows]struct{})
	}
	s.rows[rows] = struct{}{}
	return rows, nil
}
func (s *WriteSession) finish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.finishLocked(), protectMutation(s.conn, false))
}
func (s *WriteSession) finishLocked() error {
	e := s.check()
	for r := range s.rows {
		_ = r.close()
	}
	e = errors.Join(e, s.check())
	s.active = false
	return e
}

// mode 0 is caller DML, 1 permits the fixed coordinator gate write. Mode 2
// executes only fixed schema statements, never caller SQL or user callbacks.
// Mode 3 permits only the owned savepoint control statements.
func (s *WriteSession) protect(mode int) error {
	if mode == 2 {
		return protectMutation(s.conn, false)
	}
	return s.conn.Raw(func(raw any) error {
		native, ok := raw.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("recovery_sqlite_driver_required")
		}
		native.RegisterAuthorizer(func(op int, table, _ string, database string) int {
			if mode == 3 {
				if op == sqlite3.SQLITE_SAVEPOINT {
					return sqlite3.SQLITE_OK
				}
				return sqlite3.SQLITE_DENY
			}
			switch op {
			case sqlite3.SQLITE_SELECT, sqlite3.SQLITE_FUNCTION, sqliteRecursive:
				return sqlite3.SQLITE_OK
			case sqlite3.SQLITE_READ:
				if database == "main" {
					return sqlite3.SQLITE_OK
				}
			case sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE:
				if database == "main" && table != "sqlite_master" && table != "sqlite_schema" && (table != "recovery_gate" || mode == 1) {
					return sqlite3.SQLITE_OK
				}
			}
			return sqlite3.SQLITE_DENY
		})
		return nil
	})
}
func (s *WriteSession) gate() (Gate, *jsjson.Object, error) {
	var raw string
	if err := s.conn.QueryRowContext(s.ctx, "SELECT value FROM main.recovery_gate WHERE id=1").Scan(&raw); err != nil {
		return Gate{}, nil, err
	}
	g, o, e := decodeGate(raw)
	if e == nil && g.Authority != s.authority.path {
		e = errors.New("recovery_authority_not_bound")
	}
	return g, o, e
}
func (s *WriteSession) saveGate(raw *jsjson.Object) (err error) {
	text, err := jsjson.Stringify(raw)
	if err != nil {
		return err
	}
	if err = s.protect(1); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.protect(0)) }()
	_, err = s.conn.ExecContext(s.ctx, "INSERT INTO main.recovery_gate VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", text)
	return s.after(err)
}
func (s *WriteSession) ready(g Gate) (err error) {
	a := s.authority
	if err = a.mu.LockContext(s.ctx); err != nil {
		return err
	}
	defer a.mu.Unlock()
	if !a.writer {
		return errors.New("recovery_writer_required")
	}
	if g.State != "ready" || g.MinimumReader > ReaderCapability {
		return errors.New("recovery_quarantined_or_unsupported")
	}
	if err = beginOwned(s.ctx, a.conn, "BEGIN"); err != nil {
		return err
	}
	defer func() {
		e := rollbackOwned(a.conn)
		if e != nil {
			a.cached = false
		}
		err = errors.Join(err, e)
	}()
	head, err := a.current(s.ctx)
	if err == nil && head != g.Head {
		err = errors.New("recovery_authority_head_changed")
	}
	return err
}
func (s *WriteSession) AssertReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	g, _, e := s.gate()
	if e != nil {
		return e
	}
	return s.ready(g)
}
func (s *WriteSession) RequireReader(minimum int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	if minimum < 1 || minimum > ReaderCapability {
		return errors.New("recovery_write_invalid")
	}
	g, raw, e := s.gate()
	if e != nil {
		return e
	}
	if e = s.ready(g); e != nil {
		return e
	}
	if g.MinimumReader >= int64(minimum) {
		return nil
	}
	_ = raw.Set("minimumReader", float64(minimum))
	return s.saveGate(raw)
}
func (s *WriteSession) AppendIntent(input Revocation) (head Head, err error) {
	event, err := encodeRevocation(input)
	if err != nil {
		return head, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(); err != nil {
		return head, err
	}
	g, raw, err := s.gate()
	if err != nil {
		return head, err
	}
	a := s.authority
	if err = a.mu.LockContext(s.ctx); err != nil {
		return head, err
	}
	func() {
		defer a.mu.Unlock()
		if !a.writer {
			err = errors.New("recovery_writer_required")
			return
		}
		head, err = a.appendLocked(s.ctx, event, g.Head, false, nil)
	}()
	if err != nil {
		return head, err
	}
	// Replace only head, as TS object spread does; retain unknown gate fields.
	h, _ := jsjson.NewObject(jsjson.Member{Key: "instanceId", Value: head.InstanceID}, jsjson.Member{Key: "sequence", Value: float64(head.Sequence)}, jsjson.Member{Key: "hash", Value: head.Hash})
	_ = raw.Set("head", h)
	return head, s.saveGate(raw)
}

const sourceEventDDL = `CREATE TABLE IF NOT EXISTS source_event_heads(identity_key TEXT PRIMARY KEY,event_id TEXT UNIQUE NOT NULL,scene_id TEXT NOT NULL,message_id TEXT NOT NULL,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS source_event_revisions(event_id TEXT NOT NULL,revision INTEGER NOT NULL,value TEXT NOT NULL,PRIMARY KEY(event_id,revision));
CREATE TABLE IF NOT EXISTS source_event_edit_keys(event_id TEXT NOT NULL,edit_key TEXT NOT NULL,revision INTEGER NOT NULL,change_hash TEXT NOT NULL,PRIMARY KEY(event_id,edit_key));
CREATE TABLE IF NOT EXISTS source_event_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,PRIMARY KEY(scene_id,message_id));
CREATE INDEX IF NOT EXISTS idx_source_event_heads_scene_message ON source_event_heads(scene_id,message_id);`

func (s *WriteSession) EnsureSourceEventSchema() error {
	if err := s.AssertReady(); err != nil {
		return err
	}
	return s.schema(sourceEventDDL)
}
func (s *WriteSession) EnsureSourceEventLookupIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	var count int
	if e := s.conn.QueryRowContext(s.ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('source_event_heads','source_event_revisions','source_event_edit_keys','source_event_tombstones')").Scan(&count); e != nil {
		return e
	}
	if count != 4 {
		return nil
	}
	return s.schemaLocked("CREATE INDEX IF NOT EXISTS idx_source_event_heads_scene_message ON source_event_heads(scene_id,message_id)")
}
func (s *WriteSession) schema(query string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	return s.schemaLocked(query)
}
func (s *WriteSession) schemaLocked(query string) (err error) {
	if err = s.protect(2); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.protect(0)) }()
	_, err = s.conn.ExecContext(s.ctx, query)
	return s.after(err)
}

// CheckBinding rejects accidental composition with another store/database once
// at binding, rather than repeating identity checks for every SQL statement.
func (s *WriteSession) CheckBinding(a *Authority, main *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(); e != nil {
		return e
	}
	if s.authority != a || s.main != main {
		return errors.New("recovery_session_binding_mismatch")
	}
	return nil
}
