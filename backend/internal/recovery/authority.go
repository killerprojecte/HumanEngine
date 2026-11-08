// Package recovery owns durable authority verification independently from bot scheduling.
package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const ReaderCapability = 11

var authorityInstancePattern = regexp.MustCompile(`^[a-f0-9-]{36}$`)

type Head struct {
	InstanceID string `json:"instanceId"`
	Sequence   int64  `json:"sequence"`
	Hash       string `json:"hash"`
}
type Gate struct {
	Head          Head   `json:"head"`
	Authority     string `json:"authority"`
	MinimumReader int64  `json:"minimumReader"`
	State         string `json:"state"`
}
type Query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func ReadGate(ctx context.Context, q Query) (Gate, error) {
	var raw string
	var g Gate
	if err := q.QueryRowContext(ctx, "SELECT value FROM main.recovery_gate WHERE id=1").Scan(&raw); err != nil {
		return g, err
	}
	g, _, err := decodeGate(raw)
	if err != nil {
		return g, err
	}
	if g.State != "ready" || g.MinimumReader > ReaderCapability {
		return g, errors.New("recovery_gate_invalid")
	}
	return g, nil
}

type stamp struct{ data, schema, changes int64 }
type Authority struct {
	mu     authorityLock
	pool   *sql.DB
	conn   *sql.Conn
	path   string
	cached bool
	writer bool
	head   Head
	stamp  stamp
	scans  uint64
}

func Open(ctx context.Context, path string) (*Authority, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return nil, errors.New("recovery_authority_missing")
	}
	info, err := os.Lstat(filepath.Join(path, "authority.sqlite"))
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("recovery_authority_invalid")
	}
	pool, err := storage.OpenExisting(filepath.Join(path, "authority.sqlite"), true)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	a := &Authority{pool: pool, conn: conn, path: path}
	var mode string
	if err = conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		a.Close()
		return nil, errors.New("recovery_read_journal_unsupported")
	}
	return a, nil
}
func (a *Authority) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return errors.Join(a.conn.Close(), a.pool.Close())
}
func (a *Authority) Scans() uint64 { a.mu.Lock(); defer a.mu.Unlock(); return a.scans }

// Publish reserves authority before main's WAL snapshot and retains it through
// output enqueueing. emit must accept owned output synchronously, never drain a
// socket or wait for another worker. A successful emit is irrevocable even if
// later rollback fails; the returned error must not be used to replay output.
// It never calls a bot owner or waits for a main acknowledgement.
func (a *Authority) Publish(ctx context.Context, main *sql.DB, read func(*sql.Conn) ([]byte, error), emit func([]byte) error) (err error) {
	// A pool checkout is not a SQLite snapshot. Do it before authority so a
	// bounded shared pool cannot form a main-connection/authority-mutex cycle.
	conn, err := main.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = a.mu.LockContext(ctx); err != nil {
		return err
	}
	defer a.mu.Unlock()
	if err = beginOwned(ctx, a.conn, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		e := rollbackOwned(a.conn)
		if e != nil {
			a.cached = false
		}
		err = errors.Join(err, e)
	}()
	head, err := a.current(ctx)
	if err != nil {
		return err
	}
	// The full audit establishes an ordinary identity table before a write is attempted.
	// mode=rw rejects native readonly fallback; the zero-row write proves capability.
	if _, err = a.conn.ExecContext(ctx, "UPDATE main.identity SET id=id WHERE 0"); err != nil {
		return err
	}
	if err = beginOwned(ctx, conn, "BEGIN"); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollbackOwned(conn)) }()
	gate, err := ReadGate(ctx, conn)
	if err != nil {
		return err
	}
	var mainMode, authorityMode string
	if err = conn.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&mainMode); err != nil {
		return err
	}
	if err = a.conn.QueryRowContext(ctx, "PRAGMA main.journal_mode").Scan(&authorityMode); err != nil {
		return err
	}
	if mainMode != "wal" || authorityMode != "delete" {
		return errors.New("recovery_read_journal_unsupported")
	}
	if gate.Authority != a.path || gate.Head != head {
		return errors.New("recovery_authority_head_changed")
	}
	bytes, err := read(conn)
	if err != nil {
		return err
	}
	// The authority reservation still excludes every writer. The main view is
	// the same WAL snapshot used for the projection, not a fresh latest-state
	// read (a later WAL commit is allowed). Final validation mirrors the TS
	// host's observed-head/gate check immediately before its output enqueue.
	for _, owned := range []*sql.Conn{a.conn, conn} {
		active, stateErr := transactionActive(owned)
		if stateErr != nil {
			return stateErr
		}
		if !active {
			return errors.New("recovery_publication_snapshot_lost")
		}
	}
	finalGate, err := ReadGate(ctx, conn)
	if err != nil {
		return err
	}
	if finalGate != gate || finalGate.Head != head {
		return errors.New("recovery_authority_head_changed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return emit(bytes)
}
func (a *Authority) current(ctx context.Context) (Head, error) {
	var identity string
	if err := a.conn.QueryRowContext(ctx, "SELECT id FROM main.identity LIMIT 1").Scan(&identity); err != nil {
		return Head{}, err
	}
	s, err := a.readStamp(ctx)
	if err != nil {
		return Head{}, err
	}
	if a.cached && a.stamp == s {
		return a.head, nil
	}
	a.cached = false
	head, err := a.scan(ctx)
	if err != nil {
		return Head{}, err
	}
	a.head = head
	a.stamp = s
	a.cached = true
	return head, nil
}
func (a *Authority) readStamp(ctx context.Context) (stamp, error) {
	var s stamp
	for query, dst := range map[string]*int64{"PRAGMA main.data_version": &s.data, "PRAGMA main.schema_version": &s.schema, "SELECT total_changes()": &s.changes} {
		if err := a.conn.QueryRowContext(ctx, query).Scan(dst); err != nil {
			return s, err
		}
	}
	return s, nil
}
func (a *Authority) scan(ctx context.Context) (Head, error) {
	a.scans++
	expected := map[string]string{"identity": "createtableidentity(idtextprimarykey,formatintegernotnull)", "revocations": "createtablerevocations(sequenceintegerprimarykey,previous_hashtextnotnull,payloadtextnotnull,hashtextnotnull)"}
	rows, err := a.conn.QueryContext(ctx, "SELECT name,type,sql FROM main.sqlite_master WHERE name IN ('identity','revocations')")
	if err != nil {
		return Head{}, err
	}
	count := 0
	for rows.Next() {
		var name, kind, ddl string
		if err = rows.Scan(&name, &kind, &ddl); err != nil {
			rows.Close()
			return Head{}, err
		}
		normalized := strings.ToLower(strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, ddl))
		if kind != "table" || normalized != expected[name] {
			rows.Close()
			return Head{}, errors.New("recovery_schema_unsupported")
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Head{}, err
	}
	if count != 2 {
		return Head{}, errors.New("recovery_schema_unsupported")
	}
	var h Head
	var format, countIdentity int
	if err = a.conn.QueryRowContext(ctx, "SELECT id,format,(SELECT count(*) FROM main.identity) FROM main.identity").Scan(&h.InstanceID, &format, &countIdentity); err != nil {
		return h, err
	}
	if countIdentity != 1 || format != 1 {
		return h, errors.New("recovery_format_unsupported")
	}
	h.Hash = strings.Repeat("0", 64)
	rows, err = a.conn.QueryContext(ctx, "SELECT sequence,previous_hash,payload,hash FROM main.revocations ORDER BY sequence")
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var previous, payload, digest string
		if err = rows.Scan(&seq, &previous, &payload, &digest); err != nil {
			return h, err
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", h.InstanceID, seq, previous, payload)))
		if seq != h.Sequence+1 || previous != h.Hash || hex.EncodeToString(sum[:]) != digest {
			return h, errors.New("recovery_chain_invalid")
		}
		if err = ValidateEvent(payload); err != nil {
			return h, err
		}
		h.Sequence = seq
		h.Hash = digest
	}
	return h, rows.Err()
}
