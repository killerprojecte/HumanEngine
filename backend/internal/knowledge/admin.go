package knowledge

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

type AdminPublish func(context.Context, func(*assertions.Reader, func(bool) *Reader) ([]byte, error), func([]byte) error) error

// BindAdminRead borrows current assertion support and graph storage from the
// same published SQLite snapshot. It owns no model grants or write session.
func (s *Store) BindAdminRead(publisher *recovery.ReadPublisher, as *assertions.Store, cs *commitments.Store, policy assertionauthority.Options) (AdminPublish, error) {
	if s.bindingError != nil || s.maintenance != nil || s.session != nil || !publisher.OwnedBy(s.main, s.authority) {
		return nil, errors.New("knowledge_publication_binding")
	}
	publish, err := cs.BindPublishedCurrent(publisher, as, policy)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, fn func(*assertions.Reader, func(bool) *Reader) ([]byte, error), emit func([]byte) error) error {
		return publish(ctx, func(*assertions.Reader, commitments.Context) (bool, error) { return false, nil }, func(ar *assertions.Reader, support *commitments.ReadService, query commitments.PublicationQuery) ([]byte, error) {
			return fn(ar, func(enabled bool) *Reader {
				return &Reader{
					storage: &scopeStorage{check: support.AssertReady, query: func(q string, args ...any) (workingset.Rows, error) {
						return query(q, args...)
					}},
					ready: support.AssertReady, dates: policy.DateParser, now: policy.NowMillis,
					enabled: func(Context) (bool, error) { return enabled, nil },
					current: func(c Context, ref Reference) (*Assertion, error) {
						return support.CurrentAssertion(commitments.Context{SceneID: c.SceneID}, ref)
					},
				}
			})
		}, emit)
	}, nil
}

type AdminQuery struct {
	SceneID, PersonID   string
	input, anchor, seed *Object
}

func ParseAdminQuery(v any) (*AdminQuery, error) {
	anchor := func(v any) (any, error) {
		switch get(v, "kind") {
		case "person":
			return shape(v, f("kind", enumeration("person")), f("personId", idValue))
		case "memory":
			return shape(v, f("kind", enumeration("memory")), f("backingRef", ref))
		case "entity":
			return shape(v, f("kind", enumeration("entity")), f("entityId", idValue), f("backingRef", ref))
		}
		return nil, ErrSchema
	}
	depth := func(v any) (any, error) {
		if n, ok := v.(float64); ok && (n == 1 || n == 2) {
			return n, nil
		}
		return nil, ErrSchema
	}
	input, err := shape(v, f("sceneId", idValue), opt("personId", idValue), opt("seedBackingRef", ref),
		f("anchor", anchor), f("predicates", list(enumeration(predicateNames...), 1, 7)), f("depth", depth))
	if err != nil {
		return nil, err
	}
	return &AdminQuery{str(get(input, "sceneId")), str(get(input, "personId")), input, object(get(input, "anchor")), object(get(input, "seedBackingRef"))}, nil
}

type AdminAnchorError string

func (e AdminAnchorError) Error() string { return string(e) }

const (
	AdminAnchorMismatch    AdminAnchorError = "knowledge_anchor_mismatch"
	AdminSeedRequired      AdminAnchorError = "knowledge_seed_required"
	AdminAnchorUnavailable AdminAnchorError = "knowledge_anchor_unavailable"
)

func (r *Reader) AdminQuery(q *AdminQuery, enabled bool) (*Object, error) {
	ctx := Context{SceneID: q.SceneID, Provenance: "contextual"}
	switch get(q.anchor, "kind") {
	case "person":
		if get(q.anchor, "personId") != q.PersonID {
			return nil, AdminAnchorMismatch
		}
	case "memory":
		value, err := r.Discover(ctx, object(get(q.anchor, "backingRef")), q.input)
		return adminResult(value, enabled, err)
	case "entity":
		if q.seed == nil && q.PersonID == "" {
			return nil, AdminSeedRequired
		}
		all := make([]any, len(predicateNames))
		for i, p := range predicateNames {
			all[i] = p
		}
		seedInput := obj("predicates", all, "depth", float64(2))
		var value *Object
		var err error
		if q.seed != nil {
			value, err = r.Discover(ctx, q.seed, seedInput)
		} else {
			_ = seedInput.Set("anchor", obj("kind", "person", "personId", q.PersonID))
			value, err = r.Query(ctx, seedInput)
		}
		if err != nil {
			return nil, err
		}
		if get(value, "coverage") != "complete" || !adminEntityInSeed(value, q.anchor) {
			return nil, AdminAnchorUnavailable
		}
		ctx.ExposedEntities = []any{q.anchor}
	}
	value, err := r.Query(ctx, q.input)
	return adminResult(value, enabled, err)
}

func adminEntityInSeed(seed, anchor *Object) bool {
	for _, edge := range arr(get(seed, "edges")) {
		for _, point := range []any{get(edge, "subject"), get(edge, "object")} {
			if get(point, "kind") == "entity" {
				entity := get(point, "entity")
				if get(entity, "id") == get(anchor, "entityId") && sameReadRef(get(entity, "backing"), get(anchor, "backingRef")) {
					return true
				}
			}
		}
	}
	return false
}
