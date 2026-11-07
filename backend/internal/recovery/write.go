package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

// OpenWriter opens an existing enrolled authority only. It does not enroll,
// repair, replay, change the chain format, or bind an unmatched main database.
// Unlike the query preview, it enables FULL synchronous durability and installs
// the same additive payload index as the TypeScript writer.
func OpenWriter(ctx context.Context, path string) (_ *Authority, err error) {
	a, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = a.Close()
		}
	}()
	// The writable TypeScript constructor keeps the authority file private.
	if err = os.Chmod(filepath.Join(a.path, "authority.sqlite"), 0600); err != nil {
		return nil, err
	}
	if _, err = a.conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return nil, err
	}
	if err = beginOwned(ctx, a.conn, "BEGIN IMMEDIATE"); err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rollbackOwned(a.conn)) }()
	if _, err = a.conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS main.revocations_payload ON revocations(payload)"); err != nil {
		return nil, err
	}
	if _, err = a.current(ctx); err != nil {
		return nil, err
	}
	if err = commitOwned(ctx, a.conn, "authority", nil); err != nil {
		a.cached = false
		return nil, err
	}
	a.writer = true
	return a, nil
}

// Head observes durable authority under a fresh read transaction. It is useful
// for diagnosing an uncertain commit, not a permission/token to use main data.
// A main gate mismatch still requires reconciliation; never rewrite the gate
// to this head or replay a mutation merely because this method succeeded.
func (a *Authority) Head(ctx context.Context) (head Head, err error) {
	if err = a.mu.LockContext(ctx); err != nil {
		return head, err
	}
	defer a.mu.Unlock()
	if err = beginOwned(ctx, a.conn, "BEGIN"); err != nil {
		return head, err
	}
	defer func() {
		err = errors.Join(err, rollbackOwned(a.conn))
		if err != nil {
			a.cached = false
			head = Head{}
		}
	}()
	head, err = a.current(ctx)
	if err != nil {
		return head, err
	}
	var mode string
	if err = a.conn.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&mode); err != nil {
		return head, err
	}
	if mode != "delete" {
		return head, errors.New("recovery_read_journal_unsupported")
	}
	return head, nil
}

// Append commits authority intent independently of main. A returned error may
// leave durable intent; in particular ErrCommitOutcomeUnknown must never cause
// blind replay. Expected head comparison and exact payload dedup match ledger.ts.
func (a *Authority) Append(ctx context.Context, input Revocation, expected Head, force bool) (Head, error) {
	event, err := encodeRevocation(input)
	if err != nil {
		return Head{}, err
	}
	if err := a.mu.LockContext(ctx); err != nil {
		return Head{}, err
	}
	defer a.mu.Unlock()
	return a.appendLocked(ctx, event, expected, force, nil)
}
func (a *Authority) appendLocked(ctx context.Context, event writeEvent, expected Head, force bool, commit commitExec) (head Head, err error) {
	if !a.writer {
		return head, errors.New("recovery_writer_required")
	}
	if err = ctx.Err(); err != nil {
		return head, err
	}
	if err = beginOwned(ctx, a.conn, "BEGIN IMMEDIATE"); err != nil {
		return head, err
	}
	defer func() {
		cleanup := rollbackOwned(a.conn)
		err = errors.Join(err, cleanup)
		if err != nil {
			a.cached = false
			head = Head{}
		}
	}()
	if err = authorityWriteMode(ctx, a.conn); err != nil {
		return head, err
	}
	previous, err := a.current(ctx)
	if err != nil {
		return head, err
	}
	if previous != expected {
		return head, errors.New("recovery_authority_head_changed")
	}
	before := a.stamp
	head = previous
	var found bool
	if !force && !event.alwaysAppend {
		if err = a.conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM main.revocations WHERE payload=?)", event.payload).Scan(&found); err != nil {
			return head, err
		}
	}
	after := before
	if force || event.alwaysAppend || !found {
		// This is the JavaScript protocol's intrinsic safe-integer width, not a new cap.
		if previous.Sequence >= 9007199254740991 {
			return head, errors.New("recovery_sequence_unsupported")
		}
		head.Sequence++
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", head.InstanceID, head.Sequence, previous.Hash, event.payload)))
		head.Hash = hex.EncodeToString(sum[:])
		a.cached = false
		result, writeErr := a.conn.ExecContext(ctx, "INSERT INTO main.revocations(sequence,previous_hash,payload,hash) VALUES(?,?,?,?)", head.Sequence, previous.Hash, event.payload, head.Hash)
		if writeErr != nil {
			return head, writeErr
		}
		changed, changedErr := result.RowsAffected()
		if changedErr != nil {
			return head, changedErr
		}
		if after, err = a.readStamp(ctx); err != nil {
			return head, err
		}
		// sqlite total_changes includes trigger work. Only our exact single-row
		// append may extend an audited snapshot without rehashing historical rows.
		if changed != 1 || after.changes != before.changes+1 || after.schema != before.schema || after.data != before.data {
			audited, auditErr := a.scan(ctx)
			if auditErr != nil {
				return head, auditErr
			}
			if audited != head {
				return head, errors.New("recovery_authority_head_changed")
			}
		}
	}
	if err = commitOwned(ctx, a.conn, "authority", commit); err != nil {
		return head, err
	}
	// The stamp was captured under the verified snapshot, never after COMMIT:
	// an external commit immediately after unlock must force a new audit.
	a.head, a.stamp, a.cached = head, after, true
	return head, nil
}
func authorityWriteMode(ctx context.Context, conn *sql.Conn) error {
	var mode string
	var synchronous int
	if err := conn.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&mode); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA main.synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if mode != "delete" || synchronous != 2 {
		return errors.New("recovery_write_durability_unsupported")
	}
	return nil
}

// MainMutation is a short-lived, serialized main transaction capability. It
// offers no commit, raw connection, schema, gate, or external-effect operation.
// Any SQL error fails the mutation, even if the callback ignores it.
// Returned rows are closed when the callback ends; retained handles cannot write.
// Callbacks must not invoke this Authority or perform network/filesystem effects.
type MainMutation struct {
	mu     sync.Mutex
	conn   *sql.Conn
	ctx    context.Context
	active bool
	rows   []*MainRows
	failed error
}

// usableLocked rejects every operation after the first failure. In particular,
// SQLite RAISE(ROLLBACK), cancellation and I/O failures can end the native
// transaction while this Go capability still exists.
func (m *MainMutation) usableLocked() error {
	if !m.active {
		return sql.ErrTxDone
	}
	if m.failed != nil {
		return m.failed
	}
	if err := m.ctx.Err(); err != nil {
		return m.failLocked(err)
	}
	return nil
}
func (m *MainMutation) failLocked(err error) error {
	if err != nil && m.failed == nil {
		m.failed = err
	}
	return err
}
func (m *MainMutation) transactionLocked() error {
	if err := m.usableLocked(); err != nil {
		return err
	}
	active, err := transactionActive(m.conn)
	if err != nil {
		return m.failLocked(err)
	}
	if !active {
		return m.failLocked(errors.Join(sql.ErrTxDone, errors.New("recovery_main_transaction_lost")))
	}
	return nil
}
func (m *MainMutation) Exec(query string, args ...any) (sql.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.transactionLocked(); err != nil {
		return nil, err
	}
	result, err := m.conn.ExecContext(m.ctx, query, args...)
	m.failLocked(err)
	return result, err
}
func (m *MainMutation) Query(query string, args ...any) (*MainRows, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.transactionLocked(); err != nil {
		return nil, err
	}
	rows, err := m.conn.QueryContext(m.ctx, query, args...)
	if err != nil {
		return nil, m.failLocked(err)
	}
	controlled := &MainRows{owner: m, rows: rows}
	m.rows = append(m.rows, controlled)
	return controlled, nil
}
func (m *MainMutation) finish(prior error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// A lost native transaction must not turn the coordinator's later ACK into
	// an autocommit, even if the last statement returned no immediate error.
	m.transactionLocked()
	m.active = false
	for _, rows := range m.rows {
		rows.closeLocked()
	}
	failed := m.failed
	// Returning the statement error from Apply/Prepare must not duplicate it
	// merely because the capability also latched the same failure.
	if failed != nil && prior != nil && errors.Is(prior, failed) {
		failed = nil
	}
	return errors.Join(prior, failed, protectMutation(m.conn, false))
}

// MainRead reads only within PrepareWrite's reserved main snapshot. It is not
// permission to publish that data: authority is checked after preparation.
// SQL writes/transaction control are denied natively and fail the entire plan.
type MainRead struct{ state *MainMutation }

func (r *MainRead) Query(query string, args ...any) (*MainRows, error) {
	return r.state.Query(query, args...)
}

// WritePlan is consumed before releasing main's writer reservation. Apply must
// perform only its intended main SQL effects, with no network/filesystem effects
// or calls back into this Authority. Reader capability is explicit, as in TS.
type WritePlan struct {
	Intents       []Revocation
	MinimumReader int
	Apply         func(*MainMutation) error
}

// PrepareWrite reads previous values/revisions and computes intents while
// holding BEGIN IMMEDIATE on main, before taking authority's mutex. Competing
// main writers cannot invalidate its read/modify/write decision. The callback
// must do only reads and pure computation, never publish data or external effects.
// Its result is executed immediately; no snapshot/capability/token escapes.
func (a *Authority) PrepareWrite(ctx context.Context, main *sql.DB, prepare func(*MainRead) (WritePlan, error)) (Head, error) {
	if prepare == nil {
		return Head{}, errors.New("recovery_write_invalid")
	}
	return a.executeWrite(ctx, main, nil, 0, nil, prepare, writeHooks{})
}
func preparePlan(ctx context.Context, conn *sql.Conn, prepare func(*MainRead) (WritePlan, error)) (plan WritePlan, err error) {
	if err = protectPreparation(conn); err != nil {
		return plan, err
	}
	state := &MainMutation{conn: conn, ctx: ctx, active: true}
	defer func() { err = state.finish(err) }()
	return prepare(&MainRead{state: state})
}
func encodePlan(inputs []Revocation, minimumReader int, apply func(*MainMutation) error) ([]writeEvent, error) {
	if minimumReader < 1 || minimumReader > ReaderCapability || apply == nil {
		return nil, errors.New("recovery_write_invalid")
	}
	events := make([]writeEvent, len(inputs))
	for i, input := range inputs {
		event, err := encodeRevocation(input)
		if err != nil {
			return nil, err
		}
		events[i] = event
	}
	return events, nil
}

// Write reserves main's WAL writer before authority. Authority intent commits
// first; mutations and the final ACK commit together in main, not atomically
// across databases. Failure after intent leaves a gap requiring reconciliation.
// No reconciliation, restart replay, production HTTP write path, or automatic
// retry is implemented here. minimumReader is the domain's explicit capability
// requirement; append itself does not infer a capability from event kind.
func (a *Authority) Write(ctx context.Context, main *sql.DB, inputs []Revocation, minimumReader int, apply func(*MainMutation) error) (Head, error) {
	return a.write(ctx, main, inputs, minimumReader, apply, writeHooks{})
}

// Deterministic fault boundaries are package-private and never wired to runtime.
type writeHooks struct {
	afterIntent      func(Head) error
	beforeMainCommit func() error
	commit           commitExec
}

func (a *Authority) write(ctx context.Context, main *sql.DB, inputs []Revocation, minimumReader int, apply func(*MainMutation) error, hooks writeHooks) (Head, error) {
	// Materialize/validate all fixed intents before acquiring any locks.
	events, err := encodePlan(inputs, minimumReader, apply)
	if err != nil {
		return Head{}, err
	}
	return a.executeWrite(ctx, main, events, minimumReader, apply, nil, hooks)
}
func (a *Authority) executeWrite(ctx context.Context, main *sql.DB, events []writeEvent, minimumReader int, apply func(*MainMutation) error, prepare func(*MainRead) (WritePlan, error), hooks writeHooks) (head Head, err error) {
	conn, err := main.Conn(ctx)
	if err != nil {
		return head, err
	}
	defer conn.Close()
	if err = writableMain(ctx, conn, a.path); err != nil {
		return head, err
	}
	// Critically, neither authority's mutex nor its SQLite reservation is held
	// while waiting to acquire the main writer. Publication takes authority first
	// but only a WAL read snapshot, so it cannot wait on this writer reservation.
	if err = beginOwned(ctx, conn, "BEGIN IMMEDIATE"); err != nil {
		return head, err
	}
	defer func() {
		err = errors.Join(err, rollbackOwned(conn))
		if err != nil {
			head = Head{}
		}
	}()
	gate, err := ReadGate(ctx, conn)
	if err != nil {
		return head, err
	}
	if gate.Authority != a.path {
		return head, errors.New("recovery_authority_not_bound")
	}
	// BEGIN IMMEDIATE alone does not prove SQLite's handle is writable.
	if _, err = conn.ExecContext(ctx, "UPDATE main.recovery_gate SET id=id WHERE 0"); err != nil {
		return head, err
	}
	if prepare != nil {
		plan, prepareErr := preparePlan(ctx, conn, prepare)
		if prepareErr != nil {
			return head, prepareErr
		}
		events, err = encodePlan(plan.Intents, plan.MinimumReader, plan.Apply)
		if err != nil {
			return head, err
		}
		minimumReader, apply = plan.MinimumReader, plan.Apply
	}
	if err := a.mu.LockContext(ctx); err != nil {
		return Head{}, err
	}
	defer a.mu.Unlock()
	if !a.writer {
		return head, errors.New("recovery_writer_required")
	}
	head = gate.Head
	for _, event := range events {
		head, err = a.appendLocked(ctx, event, head, false, hooks.commit)
		if err != nil {
			return head, err
		}
		if hooks.afterIntent != nil {
			if err = hooks.afterIntent(head); err != nil {
				return head, err
			}
		}
	}
	// There is a real cross-database gap after intent COMMIT. Re-reserve authority
	// and compare again: another authority writer must not be silently ACKed.
	if err = beginOwned(ctx, a.conn, "BEGIN IMMEDIATE"); err != nil {
		return head, err
	}
	defer func() {
		cleanup := rollbackOwned(a.conn)
		if cleanup != nil {
			a.cached = false
		}
		err = errors.Join(err, cleanup)
	}()
	if err = authorityWriteMode(ctx, a.conn); err != nil {
		return head, err
	}
	current, err := a.current(ctx)
	if err != nil {
		return head, err
	}
	if current != head {
		return head, errors.New("recovery_authority_head_changed")
	}
	if err = protectMutation(conn, true); err != nil {
		return head, err
	}
	mutation := &MainMutation{conn: conn, ctx: ctx, active: true}
	// Also retire the capability and authorizer on callback panic before rollback.
	func() {
		defer func() { err = mutation.finish(err) }()
		err = apply(mutation)
	}()
	if err != nil {
		return head, err
	}
	if err = ctx.Err(); err != nil {
		return head, err
	}
	gate.Head = head
	if minimumReader > gate.MinimumReader {
		gate.MinimumReader = minimumReader
	}
	raw, err := json.Marshal(gate)
	if err != nil {
		return head, err
	}
	result, err := conn.ExecContext(ctx, "UPDATE main.recovery_gate SET value=? WHERE id=1", string(raw))
	if err != nil {
		return head, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return head, err
	}
	stored, err := ReadGate(ctx, conn)
	if err != nil {
		return head, err
	}
	if n != 1 || stored != gate {
		return head, errors.New("recovery_gate_invalid")
	}
	if hooks.beforeMainCommit != nil {
		if err = hooks.beforeMainCommit(); err != nil {
			return head, err
		}
	}
	if err = commitOwned(ctx, conn, "main", hooks.commit); err != nil {
		// Main's uncertain outcome is not permission to reuse any cached decision.
		a.cached = false
		return head, err
	}
	return head, nil
}
func writableMain(ctx context.Context, conn *sql.Conn, authority string) error {
	active, err := transactionActive(conn)
	if err != nil {
		return err
	}
	if active {
		return errors.New("recovery_main_transaction_active")
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return err
	}
	var path string
	for rows.Next() {
		var id int
		var name, file string
		if err = rows.Scan(&id, &name, &file); err != nil {
			rows.Close()
			return err
		}
		if name == "main" {
			path = file
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if path == "" {
		return errors.New("recovery_main_file_required")
	}
	// Match authorityPath's directory-independence rule. The authority path
	// itself was already checked for symlinks by Open; TS does not reject a
	// symlinked main filename, so do not introduce that additional restriction.
	relative, err := filepath.Rel(filepath.Dir(path), authority)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("recovery_authority_not_independent")
	}
	var mode string
	if err = conn.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return errors.New("recovery_write_journal_unsupported")
	}
	_, err = conn.ExecContext(ctx, "PRAGMA synchronous=FULL")
	return err
}

// Keep nil typed pointers out of the sealed value-event API.
func nilRevocation(event Revocation) bool {
	return event == nil || (reflect.ValueOf(event).Kind() == reflect.Pointer && reflect.ValueOf(event).IsNil())
}
