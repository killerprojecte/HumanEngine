// Package relationstorage owns transaction-local relation persistence. Reads do
// not authorize exposure; relation tables deliberately have no foreign keys.
package relationstorage

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Object = jsjson.Object
type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}
type Reader struct {
	check func() error
	query func(string, ...any) (Rows, error)
}
type Store struct {
	maintenance        *recovery.Maintenance
	maintenanceTx      *recovery.MaintenanceTx
	maintenanceContext context.Context
	main               *sql.DB
	authority          *recovery.Authority
	session            *recovery.WriteSession
	bindingError       error
}

func New(main *sql.DB, authority *recovery.Authority) (*Store, error) {
	if main == nil || authority == nil {
		return nil, errors.New("relation_authority_required")
	}
	return &Store{main: main, authority: authority}, nil
}
func (s *Store) WithSession(session *recovery.WriteSession) *Store {
	n := *s
	if s.maintenance != nil {
		n.bindingError = errors.New("relation_live_scope_required")
		return &n
	}
	n.session = session
	if session == nil {
		n.bindingError = errors.New("relation_transaction_required")
	} else {
		n.bindingError = session.CheckBinding(s.authority, s.main)
	}
	return &n
}
func (s *Store) RequireTransaction() error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.maintenance != nil {
		return s.maintenanceTx.CheckBinding(s.maintenanceContext, s.maintenance)
	}
	if s.session == nil {
		return errors.New("relation_transaction_required")
	}
	return s.session.CheckBinding(s.authority, s.main)
}
func (s *Store) Transaction(ctx context.Context, fn func(*Store) error) error {
	if s.maintenance != nil {
		if e := s.RequireTransaction(); e != nil {
			return e
		}
		if fn == nil {
			return errors.New("relation_callback_required")
		}
		return s.maintenanceTx.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { return fn(s.WithMaintenanceTx(ctx, tx)) })
	}
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil {
		return s.session.WithSavepoint(func(c *recovery.WriteSession) error { return fn(s.WithSession(c)) })
	}
	return s.authority.WithWriteSession(ctx, s.main, func(c *recovery.WriteSession) error { return fn(s.WithSession(c)) })
}
func (s *Store) Initialize(ctx context.Context) error {
	if s.maintenance != nil {
		if e := s.RequireTransaction(); e != nil {
			return e
		}
		return s.maintenanceTx.EnsureRelationStorageSchema(ctx)
	}
	if s.session != nil {
		if e := s.RequireTransaction(); e != nil {
			return e
		}
		return s.session.EnsureRelationStorageSchema()
	}
	return s.Transaction(ctx, func(b *Store) error { return b.session.EnsureRelationStorageSchema() })
}
func (s *Store) WithWriteSession(fn func(*recovery.WriteSession) error) error {
	if s.maintenance != nil {
		return errors.New("relation_live_scope_required")
	}
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	return errors.Join(fn(s.session), s.RequireTransaction())
}

// BorrowReadScope exposes only fixed domain reads, never arbitrary SQL or a connection.
func BorrowReadScope(check func() error, query func(string, ...any) (Rows, error)) (*Reader, error) {
	if check == nil || query == nil {
		return nil, errors.New("relation_read_scope_required")
	}
	if e := check(); e != nil {
		return nil, e
	}
	return &Reader{check, query}, nil
}
func (s *Store) Reader() (*Reader, error) {
	return BorrowReadScope(s.RequireTransaction, func(q string, args ...any) (Rows, error) { return s.query(q, bind(args)...) })
}
func bind(args []any) []any {
	out := append([]any{}, args...)
	for i, v := range out {
		if s, ok := v.(string); ok {
			if t, e := jsjson.UTF8Text(s); e == nil {
				out[i] = t
			}
		}
	}
	return out
}
func (r *Reader) one(q string, args []any, dest ...any) (found bool, err error) {
	if err = r.check(); err != nil {
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
func (r *Reader) value(q string, args ...any) (*Object, error) {
	var raw string
	ok, e := r.one(q, args, &raw)
	if e != nil || !ok {
		return nil, e
	}
	return parse(raw)
}
func parse(raw string) (*Object, error) {
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, errors.New("relation_record_object_required")
	}
	return o, nil
}
func (r *Reader) Entity(id string) (*Object, error) {
	return r.value("SELECT value FROM memory_relation_entities WHERE id=?", id)
}
func (r *Reader) Get(id string) (*Object, error) {
	return r.value("SELECT value FROM memory_relation_candidates WHERE id=?", id)
}
func (r *Reader) ByKey(key string) (*Object, error) {
	return r.value("SELECT value FROM memory_relation_candidates WHERE admission_key=?", key)
}
func (r *Reader) Rows(scene string) (out []*Object, err error) {
	out = []*Object{}
	if err = r.check(); err != nil {
		return
	}
	rows, err := r.query("SELECT value FROM memory_relation_candidates WHERE scene_id=? ORDER BY id LIMIT ?", scene, 201)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return
		}
		var v *Object
		v, err = parse(raw)
		if err != nil {
			return
		}
		out = append(out, v)
	}
	err = rows.Err()
	return
}
func get(v any, k string) any {
	if o, ok := v.(*Object); ok && o != nil {
		x, _ := o.Get(k)
		return x
	}
	return nil
}
func (s *Store) exec(q string, args ...any) (sql.Result, error) {
	if e := s.RequireTransaction(); e != nil {
		return nil, e
	}
	return s.execSQL(q, bind(args)...)
}
func (s *Store) Put(record *Object, key string, entity *Object) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	r, e := s.Reader()
	if e != nil {
		return e
	}
	var one int
	found, e := r.one("SELECT 1 FROM memory_relation_tombstones WHERE id=?", []any{get(record, "id")}, &one)
	if e != nil {
		return e
	}
	if found {
		return errors.New("relation_deleted")
	}
	if entity != nil {
		raw, e := jsjson.Stringify(entity)
		if e != nil {
			return e
		}
		if _, e = s.exec("INSERT INTO memory_relation_entities VALUES(?,?,?,?)", get(entity, "id"), get(entity, "sceneId"), get(get(entity, "backing"), "id"), raw); e != nil {
			return e
		}
	}
	raw, e := jsjson.Stringify(record)
	if e != nil {
		return e
	}
	_, e = s.exec("INSERT INTO memory_relation_candidates VALUES(?,?,?,?,?,?,?)", get(record, "id"), get(record, "sceneId"), get(get(record, "backing"), "id"), get(get(record, "target"), "id"), key, get(record, "version"), raw)
	return e
}
func (s *Store) Update(record *Object, expected float64) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	raw, e := jsjson.Stringify(record)
	if e != nil {
		return e
	}
	result, e := s.exec("UPDATE memory_relation_candidates SET version=?,value=? WHERE id=? AND version=?", get(record, "version"), raw, get(record, "id"), expected)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return errors.New("relation_version_conflict")
	}
	return nil
}
func (s *Store) PurgeAssertion(id string) (out []string, err error) {
	out = []string{}
	if err = s.RequireTransaction(); err != nil {
		return
	}
	rows, err := s.query("SELECT id,version FROM memory_relation_candidates WHERE assertion_id=? OR target_id IN(SELECT id FROM memory_relation_entities WHERE assertion_id=?)", bind([]any{id, id})...)
	if err != nil {
		return
	}
	type row struct {
		id      string
		version float64
	}
	selected := []row{}
	for rows.Next() {
		var v row
		if err = rows.Scan(&v.id, &v.version); err != nil {
			break
		}
		selected = append(selected, v)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return
	}
	for _, v := range selected {
		if _, err = s.exec("INSERT INTO memory_relation_tombstones VALUES(?,?) ON CONFLICT(id) DO UPDATE SET version=MAX(version,excluded.version)", v.id, v.version+1); err != nil {
			return
		}
		if _, err = s.exec("DELETE FROM memory_relation_candidates WHERE id=?", v.id); err != nil {
			return
		}
		out = append(out, v.id)
	}
	_, err = s.exec("DELETE FROM memory_relation_entities WHERE assertion_id=?", id)
	return
}
func (s *Store) AssertionChanged(id string) ([]string, error) { return s.PurgeAssertion(id) }
