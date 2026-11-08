// Package knowledge owns historical knowledge storage and privacy maintenance.
// It does not implement admission, requalification, query, or model exposure.
package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Store struct {
	maintenance        *recovery.Maintenance
	maintenanceTx      *recovery.MaintenanceTx
	maintenanceContext context.Context
	main               *sql.DB
	authority          *recovery.Authority
	session            *recovery.WriteSession
	bindingError       error
}

func NewStore(main *sql.DB, authority *recovery.Authority) (*Store, error) {
	if main == nil || authority == nil {
		return nil, errors.New("knowledge_authority_required")
	}
	return &Store{main: main, authority: authority}, nil
}
func (s *Store) WithSession(session *recovery.WriteSession) *Store {
	n := *s
	if s.maintenance != nil {
		n.bindingError = errors.New("knowledge_live_scope_required")
		return &n
	}
	n.session = session
	if session == nil {
		n.bindingError = errors.New("knowledge_transaction_required")
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
		return errors.New("knowledge_transaction_required")
	}
	if e := s.session.CheckBinding(s.authority, s.main); e != nil {
		return e
	}
	return s.session.AssertReady()
}
func (s *Store) WithWriteSession(fn func(*recovery.WriteSession) error) error {
	if s.maintenance != nil {
		return errors.New("knowledge_live_scope_required")
	}
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	return errors.Join(fn(s.session), s.RequireTransaction())
}
func (s *Store) Transaction(ctx context.Context, fn func(*Store) error) error {
	if s.maintenance != nil {
		if e := s.RequireTransaction(); e != nil {
			return e
		}
		if fn == nil {
			return errors.New("knowledge_callback_required")
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
func (s *Store) exists() (exists bool, err error) {
	if err = s.RequireTransaction(); err != nil {
		return
	}
	rows, err := s.query("SELECT 1 FROM sqlite_master WHERE name='memory_knowledge_edges' AND type='table'")
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	exists = rows.Next()
	err = rows.Err()
	return
}

// Rows preserves SQLite id order and scans every scene when scene is nil.
// The optional limit is caller-owned and is never implicitly applied to purge.
func (s *Store) Rows(scene *string, limit *int) (out []*Object, err error) {
	out = []*Object{}
	exists, err := s.exists()
	if err != nil || !exists {
		return
	}
	q := "SELECT id,scene_id,assertion_id,value FROM memory_knowledge_edges"
	args := []any{}
	if scene != nil {
		q += " WHERE scene_id=?"
		args = append(args, *scene)
	}
	q += " ORDER BY id"
	if limit != nil {
		q += " LIMIT ?"
		args = append(args, *limit)
	}
	return s.readRows(q, args...)
}
func (s *Store) readRows(q string, args ...any) (out []*Object, err error) {
	out = []*Object{}
	if err = s.RequireTransaction(); err != nil {
		return
	}
	rows, err := s.query(q, bind(args)...)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var id, scene, assertion, raw string
		if err = rows.Scan(&id, &scene, &assertion, &raw); err != nil {
			return
		}
		v, e := jsjson.Parse(raw)
		edge := object(v)
		if e != nil || edge == nil || str(get(edge, "id")) != id || str(get(edge, "sceneId")) != scene || str(get(get(edge, "backing"), "id")) != assertion {
			return nil, errors.New("knowledge_storage_corrupt")
		}
		edge, e = parseEdge(edge)
		if e != nil {
			return nil, errors.New("knowledge_storage_corrupt")
		}
		out = append(out, edge)
	}
	err = rows.Err()
	return
}
func (s *Store) lookup(column, key string) (*Object, error) {
	exists, e := s.exists()
	if e != nil || !exists {
		return nil, e
	}
	rows, e := s.readRows("SELECT id,scene_id,assertion_id,value FROM memory_knowledge_edges WHERE "+column+"=?", key)
	if e != nil || len(rows) == 0 {
		return nil, e
	}
	return rows[0], nil
}
func (s *Store) Get(id string) (*Object, error)    { return s.lookup("id", id) }
func (s *Store) ByKey(key string) (*Object, error) { return s.lookup("admission_key", key) }
func (s *Store) Entity(id, scene string) (*Object, error) {
	limit := 201
	rows, e := s.Rows(&scene, &limit)
	if e != nil {
		return nil, e
	}
	if len(rows) > 200 {
		return nil, errors.New("knowledge_scan_limit")
	}
	for _, edge := range rows {
		for _, point := range []any{get(edge, "subject"), get(edge, "object")} {
			entity := get(point, "entity")
			if str(get(point, "kind")) == "entity" && str(get(entity, "id")) == id && deepEqual(get(entity, "originEdgeId"), get(edge, "id")) {
				return object(entity), nil
			}
		}
	}
	return nil, nil
}

// A derived write raises the compatibility barrier but grants no independent intent.
func (s *Store) writeBoundary() error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	return s.requireReader(7)
}
func (s *Store) Put(edge *Object, key string) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	safe, e := parseEdge(edge)
	if e != nil {
		return e
	}
	if e := s.writeBoundary(); e != nil {
		return e
	}
	if e := s.ensureKnowledgeSchema(); e != nil {
		return e
	}
	raw, e := jsjson.Stringify(safe)
	if e != nil {
		return e
	}
	_, e = s.execSQL("INSERT INTO memory_knowledge_edges VALUES(?,?,?,?,?)", bind([]any{get(edge, "id"), get(edge, "sceneId"), get(get(edge, "backing"), "id"), key, raw})...)
	return e
}
func (s *Store) Replace(edge *Object) error {
	if e := s.RequireTransaction(); e != nil {
		return e
	}
	safe, e := parseEdge(edge)
	if e != nil {
		return e
	}
	if e := s.writeBoundary(); e != nil {
		return e
	}
	raw, e := jsjson.Stringify(safe)
	if e != nil {
		return e
	}
	_, e = s.execSQL("UPDATE memory_knowledge_edges SET value=? WHERE id=?", bind([]any{raw, get(edge, "id")})...)
	return e
}
func (s *Store) Remove(id string) error {
	exists, e := s.exists()
	if e != nil || !exists {
		return e
	}
	if e = s.writeBoundary(); e != nil {
		return e
	}
	_, e = s.execSQL("DELETE FROM memory_knowledge_edges WHERE id=?", bind([]any{id})...)
	return e
}
