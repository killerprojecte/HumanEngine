package knowledge

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

// ReadOptions carries actual captured settings separately from commitment policy.
// Authority options retain the configured native locale and Date.parse policies.
type ReadOptions struct {
	KnowledgeEnabled  bool
	CommitmentEnabled bool
	Authority         assertionauthority.Options
}

// ReadWorkingSetCurrent composes real current assertion authority and knowledge
// SQL within one trusted concrete working-set owner. The callback expires before
// the owner exits, and cannot acquire a second publication or connection.
func (s *Store) ReadWorkingSetCurrent(ctx context.Context, scope *workingset.Scope, assertionStore *assertions.Store, commitmentStore *commitments.Store, options ReadOptions, fn func(*Reader) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if fn == nil {
		return errors.New("knowledge_read_operation_required")
	}
	return s.ReadWorkingSetEpisode(ctx, scope, assertionStore, commitmentStore, options, func(reader *Reader, _ *commitments.ReadService) error {
		return fn(reader)
	})
}

// scopeStorage is deliberately not Store.WithSession: native read-only scopes
// stay read-only. Store.Rows and all mutation methods still require write ownership.
type scopeStorage struct {
	scope *workingset.Scope
	check func() error
}

func (s *scopeStorage) exists() (exists bool, err error) {
	if err = s.check(); err != nil {
		return
	}
	rows, err := s.scope.Query("SELECT 1 FROM sqlite_master WHERE name='memory_knowledge_edges' AND type='table'")
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	exists = rows.Next()
	err = rows.Err()
	return
}
func (s *scopeStorage) readRows(q string, args ...any) (out []*Object, err error) {
	out = []*Object{}
	if err = s.check(); err != nil {
		return
	}
	rows, err := s.scope.Query(q, bind(args)...)
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
		if e != nil || edge == nil || get(edge, "id") != id || get(edge, "sceneId") != scene || get(get(edge, "backing"), "id") != assertion {
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
func (s *scopeStorage) Rows(scene *string, limit *int) ([]*Object, error) {
	exists, e := s.exists()
	if e != nil || !exists {
		return []*Object{}, e
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
func (s *scopeStorage) Get(id string) (*Object, error) {
	exists, e := s.exists()
	if e != nil || !exists {
		return nil, e
	}
	rows, e := s.readRows("SELECT id,scene_id,assertion_id,value FROM memory_knowledge_edges WHERE id=?", id)
	if e != nil || len(rows) == 0 {
		return nil, e
	}
	return rows[0], nil
}
func (s *scopeStorage) Entity(id, scene string) (*Object, error) {
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
			if get(point, "kind") == "entity" && get(entity, "id") == id && deepEqual(get(entity, "originEdgeId"), get(edge, "id")) {
				return object(entity), nil
			}
		}
	}
	return nil, nil
}
