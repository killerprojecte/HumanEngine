package commitments

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sync/atomic"
	"time"
)

type Options struct {
	// Compare is the source deployment's localeCompare, not a lexical fallback.
	Compare    func(string, string) (int, error)
	DateParser *jsdate.Parser
	Now        func() string
}
type Store struct {
	main         *sql.DB
	authority    *recovery.Authority
	session      *recovery.WriteSession
	maintenance  *maintenanceScope
	bindingError error
	options      Options
}
type rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}
type Reader struct {
	check   func() error
	query   func(string, ...any) (rows, error)
	ready   func() error
	options Options
}

func New(main *sql.DB, authority *recovery.Authority, options Options) (*Store, error) {
	if main == nil || authority == nil {
		return nil, errors.New("commitment_authority_required")
	}
	if options.Compare == nil || options.DateParser == nil {
		return nil, errors.New("commitment_time_collation_required")
	}
	return &Store{main: main, authority: authority, options: options}, nil
}
func (s *Store) WithSession(session *recovery.WriteSession) *Store {
	n := *s
	n.session = session
	if s.maintenance != nil {
		n.bindingError = errors.New("commitment_live_session_required")
		return &n
	}
	if session == nil {
		n.bindingError = errors.New("commitment_transaction_required")
	} else {
		n.bindingError = session.CheckBinding(s.authority, s.main)
	}
	return &n
}
func (s *Store) requireTransaction() error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.maintenance != nil {
		return s.maintenance.check()
	}
	if s.session == nil {
		return errors.New("commitment_transaction_required")
	}
	return s.session.CheckBinding(s.authority, s.main)
}
func (s *Store) ready() error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if s.maintenance != nil {
		return nil
	}
	return s.session.AssertReady()
}
func (s *Store) Transaction(ctx context.Context, fn func(*Store) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if fn == nil {
		return errors.New("commitment_write_operation_required")
	}
	if s.maintenance != nil {
		if e := s.requireTransaction(); e != nil {
			return e
		}
		return s.maintenance.tx.Apply(ctx, 1, func(child *recovery.MaintenanceTx) error { return fn(s.withMaintenanceChild(ctx, child)) })
	}
	if s.session != nil {
		return s.session.WithSavepoint(func(child *recovery.WriteSession) error { return fn(s.WithSession(child)) })
	}
	return s.authority.WithWriteSession(ctx, s.main, func(child *recovery.WriteSession) error { return fn(s.WithSession(child)) })
}

// Initialize creates only the three base tables, in the source constructor's
// owned transaction. Constructing and reading this store never initializes it.
func (s *Store) Initialize(ctx context.Context) error {
	if s.maintenance != nil {
		return errors.New("commitment_offline_initialization_forbidden")
	}
	if s.session != nil {
		if e := s.requireTransaction(); e != nil {
			return e
		}
		return s.session.EnsureCommitmentStorageSchema()
	}
	return s.Transaction(ctx, func(b *Store) error { return b.session.EnsureCommitmentStorageSchema() })
}

// WithWriteSession is a trusted domain-owner bridge, not a plugin capability.
func (s *Store) WithWriteSession(fn func(*recovery.WriteSession) error) error {
	if s.maintenance != nil {
		return errors.New("commitment_live_session_required")
	}
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if fn == nil {
		return errors.New("commitment_write_operation_required")
	}
	e := fn(s.session)
	return errors.Join(e, s.requireTransaction())
}
func bind(args []any) []any {
	out := make([]any, len(args))
	for i, v := range args {
		if x, ok := v.(string); ok {
			if text, e := jsjson.UTF8Text(x); e == nil {
				v = text
			}
		}
		out[i] = v
	}
	return out
}
func (s *Store) reader() *Reader {
	return &Reader{check: s.requireTransaction, ready: s.ready, options: s.options, query: func(q string, args ...any) (rows, error) {
		if e := s.requireTransaction(); e != nil {
			return nil, e
		}
		if s.maintenance != nil {
			return s.maintenance.tx.QueryContext(s.maintenance.ctx, q, bind(args)...)
		}
		return s.session.Query(q, bind(args)...)
	}}
}
func (s *Store) Read(ctx context.Context, fn func(*Reader) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if fn == nil {
		return errors.New("commitment_read_operation_required")
	}
	if s.session != nil || s.maintenance != nil {
		return fn(s.reader())
	}
	return s.authority.Publish(ctx, s.main, func(conn *sql.Conn) ([]byte, error) {
		var active atomic.Bool
		active.Store(true)
		defer active.Store(false)
		ready := func() error {
			if !active.Load() {
				return sql.ErrTxDone
			}
			return ctx.Err()
		}
		r := &Reader{check: ready, ready: ready, options: s.options, query: func(q string, args ...any) (rows, error) {
			if e := ready(); e != nil {
				return nil, e
			}
			return conn.QueryContext(ctx, q, bind(args)...)
		}}
		return nil, fn(r)
	}, func([]byte) error { return nil })
}
func (r *Reader) one(q string, args []any, dest ...any) (found bool, err error) {
	if err = r.ready(); err != nil {
		return
	}
	rows, err := r.query(q, args...)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if rows.Next() {
		return true, rows.Scan(dest...)
	}
	return false, rows.Err()
}
func (r *Reader) strings(q string, args ...any) (out []string, err error) {
	out = []string{}
	if err = r.ready(); err != nil {
		return
	}
	rows, err := r.query(q, args...)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var raw any
		if err = rows.Scan(&raw); err != nil {
			return
		}
		value, ok := raw.(string)
		if !ok {
			return nil, ErrCorrupt
		}
		out = append(out, value)
	}
	err = rows.Err()
	return
}
func (s *Store) exec(q string, args ...any) error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if s.maintenance != nil {
		_, e := s.maintenance.tx.ExecContext(s.maintenance.ctx, q, bind(args)...)
		return e
	}
	_, e := s.session.Exec(q, bind(args)...)
	return e
}
func (r *Reader) Head(id string) (*Object, error) {
	var scene any
	var raw string
	found, e := r.one("SELECT scene_id,value FROM memory_commitment_heads WHERE id=?", []any{id}, &scene, &raw)
	if e != nil || !found {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	head, e := NormalizeHead(v)
	if e != nil {
		return nil, e
	}
	if get(head, "id") != id || get(head, "sceneId") != scene {
		return nil, ErrCorrupt
	}
	return head, nil
}
func (r *Reader) Revision(id string, version float64) (*Object, error) {
	var raw string
	found, e := r.one("SELECT value FROM memory_commitment_revisions WHERE id=? AND version=?", []any{id, version}, &raw)
	if e != nil || !found {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	revision, e := NormalizeRevision(v)
	if e != nil {
		return nil, e
	}
	head, e := r.Head(id)
	if e != nil {
		return nil, e
	}
	ref := get(revision, "ref")
	if get(ref, "id") != id || get(ref, "version") != version || head == nil || get(revision, "sceneId") != get(head, "sceneId") || get(ref, "deletionEpoch") != get(head, "deletionEpoch") {
		return nil, ErrCorrupt
	}
	return revision, nil
}
func (r *Reader) heads(q string, args ...any) ([]any, error) {
	ids, e := r.strings(q, args...)
	if e != nil {
		return nil, e
	}
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		h, e := r.Head(id)
		if e != nil {
			return nil, e
		}
		if h == nil {
			return nil, ErrCorrupt
		}
		out = append(out, h)
	}
	return out, nil
}
func (r *Reader) AllHeads() ([]any, error) { return r.heads("SELECT id FROM memory_commitment_heads") }
func (r *Reader) Heads(scene string) ([]any, error) {
	heads, e := r.heads("SELECT id FROM memory_commitment_heads WHERE scene_id=? ORDER BY rowid DESC LIMIT 201", scene)
	if e != nil {
		return nil, e
	}
	for _, h := range heads {
		if get(h, "sceneId") != scene {
			return nil, ErrCorrupt
		}
	}
	return heads, nil
}
func (r *Reader) Revisions(id string) (out []any, err error) {
	out = []any{}
	if err = r.ready(); err != nil {
		return
	}
	rows, err := r.query("SELECT version FROM memory_commitment_revisions WHERE id=? ORDER BY version", id)
	if err != nil {
		return
	}
	versions := []float64{}
	for rows.Next() {
		var raw any
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return
		}
		version, ok := sqliteNumber(raw)
		if !ok {
			_ = rows.Close()
			return nil, ErrCorrupt
		}
		versions = append(versions, version)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return
	}
	for _, v := range versions {
		x, e := r.Revision(id, v)
		if e != nil {
			return nil, e
		}
		if x == nil {
			return nil, ErrCorrupt
		}
		out = append(out, x)
	}
	return
}
func (r *Reader) Command(key string) (*Object, error) {
	var id string
	var version float64
	found, e := r.one("SELECT id,version FROM memory_commitment_commands WHERE key=?", []any{key}, &id, &version)
	if e != nil || !found {
		return nil, e
	}
	return obj("id", id, "version", version), nil
}
func (s *Store) Put(head, revision *Object) error {
	if e := s.ready(); e != nil {
		return e
	}
	if _, e := NormalizeHead(head); e != nil {
		return e
	}
	if revision != nil {
		if _, e := NormalizeRevision(revision); e != nil {
			return e
		}
		ref := get(revision, "ref")
		if get(ref, "id") != get(head, "id") || get(revision, "sceneId") != get(head, "sceneId") || get(ref, "deletionEpoch") != get(head, "deletionEpoch") {
			return ErrCorrupt
		}
		prior, e := s.reader().Revision(str(get(head, "id")), num(get(ref, "version")))
		if e != nil {
			return e
		}
		if prior != nil && (!deepEqual(get(prior, "terms"), get(revision, "terms")) || !deepEqual(get(prior, "ref"), ref) || get(prior, "sceneId") != get(revision, "sceneId") || get(prior, "recordedAt") != get(revision, "recordedAt")) {
			return errors.New("commitment_immutable_terms")
		}
	}
	raw, e := jsjson.Stringify(head)
	if e != nil {
		return e
	}
	if e = s.exec("INSERT INTO memory_commitment_heads VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", get(head, "id"), get(head, "sceneId"), raw); e != nil {
		return e
	}
	if revision != nil {
		raw, e = jsjson.Stringify(revision)
		if e != nil {
			return e
		}
		return s.exec("INSERT INTO memory_commitment_revisions VALUES(?,?,?) ON CONFLICT(id,version) DO UPDATE SET value=excluded.value", get(head, "id"), get(get(revision, "ref"), "version"), raw)
	}
	return nil
}
func (s *Store) Remember(key, id string, version float64) error {
	if e := s.ready(); e != nil {
		return e
	}
	return s.exec("INSERT INTO memory_commitment_commands VALUES(?,?,?)", key, id, version)
}
func (s *Store) RemoveRevision(id string, version float64) error {
	if e := s.ready(); e != nil {
		return e
	}
	if e := s.exec("DELETE FROM memory_commitment_revisions WHERE id=? AND version=?", id, version); e != nil {
		return e
	}
	return s.exec("DELETE FROM memory_commitment_commands WHERE id=? AND version=?", id, version)
}
func (s *Store) Clear(id string) error {
	if e := s.ready(); e != nil {
		return e
	}
	if e := s.exec("DELETE FROM memory_commitment_revisions WHERE id=?", id); e != nil {
		return e
	}
	return s.exec("DELETE FROM memory_commitment_commands WHERE id=?", id)
}
func (r *Reader) Snapshot(id string) (*Object, error) {
	head, e := r.Head(id)
	if e != nil || head == nil {
		return nil, e
	}
	revisions, e := r.Revisions(id)
	if e != nil {
		return nil, e
	}
	timeline, e := r.Timeline(id)
	if e != nil {
		return nil, e
	}
	hash, e := TemporalQualification(head, revisions, timeline, r.options.Compare)
	if e != nil {
		return nil, e
	}
	out := obj("head", head, "revisions", revisions)
	if timeline != nil {
		_ = out.Set("timeline", timeline)
	}
	_ = out.Set("qualificationHash", hash)
	return out, nil
}
func (s *Store) now() string {
	if s.options.Now != nil {
		return s.options.Now()
	}
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func sqliteNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}
