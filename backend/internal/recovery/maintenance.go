package recovery

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Maintenance is an offline capability for one caller-owned staging connection.
// The caller must exclusively own conn for the capability's lifetime, check it
// out before admission, and never use it for live operations. Callbacks are
// trusted domain code: they must not control transactions, alter recovery_gate,
// call Authority methods, or perform external effects. No authority intent or
// global ACK is written here. Close the caller's connection when finished.
type Maintenance struct {
	conn      *sql.Conn
	authority *Authority
	database  string
	initial   Head
	gate      Head
	mu        authorityLock
	failed    error
	sqlMu     sync.Mutex
	effects   *[]string
}

// MaintenanceTx permits domain composition on the same transaction. Apply uses
// a savepoint, so an inner failure does not undo earlier outer work. The handle
// expires when its callback returns. SQL remains owned by the caller.
type MaintenanceTx struct {
	m          *Maintenance
	active     bool
	failed     error
	rows       []*MaintenanceRows
	statements []*MaintenanceStatement
}

// Statement methods reject use after callback completion or native transaction
// loss (including a nested trigger's RAISE(ROLLBACK)). A lost transaction is
// latched for the entire outer effect, even if a callback catches its error.
func (t *MaintenanceTx) usable(ctx context.Context) error {
	if !t.active {
		return sql.ErrTxDone
	}
	if t.failed != nil {
		return t.failed
	}
	if t.m.failed != nil {
		return t.m.failed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	active, err := transactionActive(t.m.conn)
	if err != nil || !active {
		t.m.failed = errors.Join(err, errors.New("recovery_main_transaction_lost"))
		return t.m.failed
	}
	return nil
}
func (t *MaintenanceTx) fail(err error) error {
	if err != nil {
		active, stateErr := transactionActive(t.m.conn)
		if stateErr != nil || !active {
			t.m.failed = errors.Join(stateErr, errors.New("recovery_main_transaction_lost"))
		}
		// A caught ordinary statement error leaves SQLite transaction usable.
		// Only native transaction loss is poisoned across owner scopes.
	}
	return err
}
func (t *MaintenanceTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	t.m.sqlMu.Lock()
	defer t.m.sqlMu.Unlock()
	if err := t.usable(ctx); err != nil {
		return nil, err
	}
	result, err := t.m.conn.ExecContext(ctx, query, args...)
	return result, t.fail(err)
}
func (t *MaintenanceTx) QueryContext(ctx context.Context, query string, args ...any) (*MaintenanceRows, error) {
	t.m.sqlMu.Lock()
	defer t.m.sqlMu.Unlock()
	if err := t.usable(ctx); err != nil {
		return nil, err
	}
	rows, err := t.m.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, t.fail(err)
	}
	result := &MaintenanceRows{owner: t, rows: rows, ctx: ctx}
	t.rows = append(t.rows, result)
	return result, nil
}
func (t *MaintenanceTx) QueryRowContext(ctx context.Context, query string, args ...any) *MaintenanceRow {
	return &MaintenanceRow{owner: t, ctx: ctx, query: query, args: args}
}
func (t *MaintenanceTx) finish(prior error) error {
	t.m.sqlMu.Lock()
	defer t.m.sqlMu.Unlock()
	for _, rows := range t.rows {
		prior = errors.Join(prior, rows.closeLocked())
	}
	for _, statement := range t.statements {
		prior = errors.Join(prior, statement.closeLocked())
	}
	prior = errors.Join(prior, t.usable(context.Background()), t.m.failed)
	t.active = false
	return prior
}
func (t *MaintenanceTx) Apply(ctx context.Context, minimumReader int, fn func(*MaintenanceTx) error) error {
	if err := t.usable(ctx); err != nil {
		return err
	}
	return t.m.applyTransaction(ctx, minimumReader, fn, false)
}

// AdmitMaintenance independently audits authority in a controlled snapshot and
// verifies the staging prefix. A diagnostic Head is deliberately not an input.
// Admission itself neither upgrades the reader nor changes quarantine.
func AdmitMaintenance(ctx context.Context, conn *sql.Conn, database string, authority *Authority) (m *Maintenance, err error) {
	if conn == nil || authority == nil {
		return nil, errors.New("recovery_maintenance_denied")
	}
	absolute, err := filepath.Abs(database)
	if err != nil {
		return nil, err
	}
	if err = independentMaintenancePath(absolute, authority.path); err != nil {
		return nil, err
	}
	m = &Maintenance{conn: conn, authority: authority, database: absolute}
	err = m.snapshot(ctx, func(head Head) error {
		g, _, e := m.checkGate(ctx, head)
		if e != nil {
			return e
		}
		m.initial, m.gate = head, g.Head
		return nil
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// Connection returns the admitted caller-owned staging connection for domain
// reads. Mutations must run through Apply so reader upgrades are transactional.
func (m *Maintenance) Connection() *sql.Conn { return m.conn }

// Apply runs one offline effect. Prior successful calls remain committed on a
// later failure. If the caller already owns a transaction, only a savepoint is
// released; the caller still decides whether to commit that outer transaction.
func (m *Maintenance) Apply(ctx context.Context, minimumReader int, fn func(*MaintenanceTx) error) error {
	if err := m.mu.LockContext(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	m.failed = nil
	return m.applyTransaction(ctx, minimumReader, fn, true)
}
func (m *Maintenance) snapshot(ctx context.Context, fn func(Head) error) (err error) {
	a := m.authority
	if err = a.mu.LockContext(ctx); err != nil {
		return err
	}
	defer a.mu.Unlock()
	if err = beginOwned(ctx, a.conn, "BEGIN"); err != nil {
		return err
	}
	defer func() {
		cleanup := rollbackOwned(a.conn)
		err = errors.Join(err, cleanup)
		if cleanup != nil {
			a.cached = false
		}
	}()
	head, err := a.current(ctx)
	if err != nil {
		return err
	}
	var mode string
	if err = a.conn.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return errors.New("recovery_read_journal_unsupported")
	}
	return fn(head)
}
func independentMaintenancePath(database, authority string) error {
	rel, err := filepath.Rel(filepath.Dir(database), authority)
	if err != nil {
		return err
	}
	// Match ledger.ts inside(), including its lexical '..' prefix behavior.
	if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
		return errors.New("recovery_authority_not_independent")
	}
	for p := authority; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("recovery_symlink_denied")
		}
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
func maintenanceMetadata(ctx context.Context, conn *sql.Conn) (g Gate, raw *jsjson.Object, err error) {
	var exists bool
	if err = conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM main.sqlite_master WHERE type='table' AND name='recovery_gate')").Scan(&exists); err != nil {
		return
	}
	if !exists {
		err = errors.New("recovery_maintenance_denied")
		return
	}
	var value string
	if err = conn.QueryRowContext(ctx, "SELECT value FROM main.recovery_gate WHERE id=1").Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.New("recovery_gate_invalid")
		}
		return
	}
	g, raw, err = decodeGate(value)
	return
}
func (m *Maintenance) checkGate(ctx context.Context, head Head) (g Gate, raw *jsjson.Object, err error) {
	// Verify the supplied filename describes this very connection, not a token
	// for an unrelated DB or an in-memory connection.
	var seq int
	var name, file string
	rows, err := m.conn.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return g, nil, err
	}
	found := false
	for rows.Next() {
		if err = rows.Scan(&seq, &name, &file); err != nil {
			break
		}
		if name == "main" {
			found = file == m.database
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return g, nil, err
	}
	if !found {
		return g, nil, errors.New("recovery_maintenance_denied")
	}
	if err = independentMaintenancePath(m.database, m.authority.path); err != nil {
		return
	}
	g, raw, err = maintenanceMetadata(ctx, m.conn)
	if err != nil {
		return
	}
	if g.State != "quarantine" || g.MinimumReader > ReaderCapability || g.Authority != m.authority.path || g.Head.InstanceID != head.InstanceID {
		err = errors.New("recovery_maintenance_denied")
		return
	}
	if g.Head.Sequence > head.Sequence {
		err = errors.New("recovery_instance_or_head_mismatch")
		return
	}
	prefix := strings.Repeat("0", 64)
	if g.Head.Sequence > 0 {
		err = m.authority.conn.QueryRowContext(ctx, "SELECT hash FROM main.revocations WHERE sequence=?", g.Head.Sequence).Scan(&prefix)
		if err != nil {
			return
		}
	}
	if g.Head.Hash != prefix {
		err = errors.New("recovery_prefix_invalid")
	}
	return
}
func (m *Maintenance) applyTransaction(ctx context.Context, minimumReader int, fn func(*MaintenanceTx) error, acquireAuthority bool) (err error) {
	if minimumReader < 1 || minimumReader > ReaderCapability {
		return errors.New("recovery_reader_unsupported")
	}
	nested, err := transactionActive(m.conn)
	if err != nil {
		return err
	}
	// SQLite resolves duplicate savepoint names to the most recent one.
	const name = "recovery_offline_effect"
	if nested {
		_, err = m.conn.ExecContext(ctx, "SAVEPOINT "+name)
	} else {
		err = beginOwned(ctx, m.conn, "BEGIN IMMEDIATE")
	}
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if done {
			return
		}
		if !nested {
			err = errors.Join(err, rollbackOwned(m.conn))
			return
		}
		active, e := transactionActive(m.conn)
		if e != nil {
			err = errors.Join(err, e)
			return
		}
		if active {
			_, e = m.conn.ExecContext(context.Background(), "ROLLBACK TO "+name)
			err = errors.Join(err, e)
			_, e = m.conn.ExecContext(context.Background(), "RELEASE "+name)
			err = errors.Join(err, e)
		}
	}()
	// SAVEPOINT does not reserve a deferred caller transaction. Obtain its
	// write reservation before acquiring authority, without changing any rows
	// or committing the caller's transaction. SQLite has row triggers only.
	if nested && acquireAuthority {
		if _, err = m.conn.ExecContext(ctx, "UPDATE main.recovery_gate SET value=value WHERE 0"); err != nil {
			return err
		}
	}
	run := func() (err error) {
		g, raw, err := m.checkGate(ctx, m.initial)
		if err != nil {
			return err
		}
		if g.Head != m.gate {
			return errors.New("recovery_prefix_invalid")
		}
		if g.MinimumReader < int64(minimumReader) {
			if e := raw.Set("minimumReader", float64(minimumReader)); e != nil {
				return e
			}
			value, e := jsjson.Stringify(raw)
			if e != nil {
				return e
			}
			if _, err = m.conn.ExecContext(ctx, "UPDATE main.recovery_gate SET value=? WHERE id=1", string(value)); err != nil {
				return err
			}
		}
		tx := &MaintenanceTx{m: m, active: true}
		func() { defer func() { err = tx.finish(err) }(); err = fn(tx) }()
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		after, _, err := m.checkGate(ctx, m.initial)
		if err != nil {
			return err
		}
		if after.Head != m.gate || after.MinimumReader < g.MinimumReader || after.MinimumReader < int64(minimumReader) {
			return errors.New("recovery_gate_invalid")
		}
		active, err := transactionActive(m.conn)
		if err != nil {
			return err
		}
		if !active {
			return errors.New("recovery_main_transaction_lost")
		}
		if nested {
			_, err = m.conn.ExecContext(ctx, "RELEASE "+name)
		} else {
			err = commitOwned(ctx, m.conn, "maintenance", nil)
		}
		return err
	}
	if acquireAuthority {
		err = m.snapshot(ctx, func(head Head) error {
			if head != m.initial {
				return errors.New("recovery_authority_head_changed")
			}
			return run()
		})
	} else {
		err = run()
	}
	done = err == nil
	return err
}
