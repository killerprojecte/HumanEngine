package offlinelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
)

type semanticReplay struct {
	owner  *recovery.Maintenance
	memory *memoryservice.OfflineLifecycle
	latest map[string]bool
}

func (p *semanticReplay) supported(ctx context.Context, tx *recovery.MaintenanceTx) (*semantic.Store, error) {
	_, e := schemas(ctx, tx, "memory_semantic_%", map[string][]string{"memory_semantic_candidates": {"id", "scene_id", "input_key", "version", "value"}, "memory_semantic_attempts": {"scene_id", "input_key", "value"}}, false, "recovery_unknown_semantic_schema")
	if e != nil {
		return nil, e
	}
	s, e := semantic.NewMaintenanceStore(p.owner, tx, ctx)
	if e != nil {
		return nil, e
	}
	if _, e = s.Rows(nil); e != nil {
		return nil, errors.New("recovery_semantic_material_invalid")
	}
	if _, e = s.Attempts(); e != nil {
		return nil, errors.New("recovery_semantic_material_invalid")
	}
	return s, nil
}
func (p *semanticReplay) Prepare(ctx context.Context, events []string) error {
	p.latest = map[string]bool{}
	return p.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		s, e := p.supported(ctx, tx)
		if e != nil {
			return e
		}
		values, e := finalEvents(events, func(v *object) string {
			if get(v, "kind") == "semantic_mutation" && get(v, "operation") != "attempt" {
				return str(get(v, "id"))
			}
			return ""
		})
		if e != nil {
			return e
		}
		for _, v := range values {
			current, e := s.Get(str(get(v, "id")))
			if e != nil {
				return e
			}
			if current == nil {
				continue
			}
			h, e := semantic.Hash(current)
			if e != nil {
				return e
			}
			if get(current, "sceneId") == get(v, "sceneId") && get(current, "version") == get(v, "nextVersion") && h == get(v, "afterHash") {
				p.latest[str(get(v, "id"))] = true
			}
		}
		return nil
	})
}
func (p *semanticReplay) Apply(ctx context.Context, raw string) error {
	v, e := event(raw)
	if e != nil {
		return e
	}
	id := str(get(v, "id"))
	if p.latest[id] {
		return nil
	}
	operation := get(v, "operation")
	if operation != "review" && !(operation == "privacy" && get(v, "afterHash") == nil) {
		return nil
	}
	return p.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		s, e := semantic.NewMaintenanceStore(p.owner, tx, ctx)
		if e != nil {
			return e
		}
		current, e := s.Get(id)
		if e != nil {
			return e
		}
		if operation == "privacy" && current == nil {
			return nil
		}
		missing := "recovery_semantic_review_material_missing"
		if operation == "privacy" {
			missing = "recovery_semantic_delete_material_missing"
		}
		if current == nil {
			return errors.New(missing)
		}
		h, e := semantic.Hash(current)
		if e != nil {
			return e
		}
		if get(current, "sceneId") != get(v, "sceneId") || get(current, "version") != get(v, "previousVersion") || h != get(v, "beforeHash") {
			return errors.New(missing)
		}
		if operation == "privacy" {
			return s.Erase(semantic.Context{SceneID: str(get(v, "sceneId"))}, id)
		}
		status := str(get(obj(get(v, "nextState")), "status"))
		if status != "admitted" && status != "rejected" {
			return errors.New("recovery_semantic_event_invalid")
		}
		next, e := p.memory.ReviewSemantic(ctx, tx, str(get(v, "sceneId")), id, get(v, "previousVersion").(float64), status)
		if e != nil {
			return e
		}
		h, e = semantic.Hash(next)
		if e != nil {
			return e
		}
		if get(next, "version") != get(v, "nextVersion") || h != get(v, "afterHash") {
			return errors.New("recovery_semantic_head_mismatch")
		}
		return nil
	})
}
func (p *semanticReplay) Verify(ctx context.Context, tx *recovery.MaintenanceTx, events []string) error {
	s, e := p.supported(ctx, tx)
	if e != nil {
		return e
	}
	latest, e := finalEvents(events, func(v *object) string {
		if get(v, "kind") != "semantic_mutation" {
			return ""
		}
		kind := "candidate"
		if get(v, "operation") == "attempt" {
			kind = "attempt"
		}
		key, _ := jsjson.Stringify([]any{kind, get(v, "sceneId"), get(v, "id")})
		return key
	})
	if e != nil {
		return e
	}
	attempts, e := s.Attempts()
	if e != nil {
		return e
	}
	for _, v := range latest {
		if get(v, "operation") == "attempt" {
			var current *object
			for _, a := range attempts {
				if get(a, "sceneId") == get(v, "sceneId") && get(a, "inputKey") == get(v, "id") {
					current = a
					break
				}
			}
			if current == nil {
				return errors.New("recovery_semantic_attempt_material_missing")
			}
			h, e := semantic.Hash(current)
			if e != nil {
				return e
			}
			if get(current, "version") != get(v, "nextVersion") || h != get(v, "afterHash") {
				return errors.New("recovery_semantic_attempt_mismatch")
			}
		} else if get(v, "afterHash") == nil {
			var exists bool
			if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_semantic_candidates')").Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return errors.New("recovery_semantic_delete_incomplete")
			}
			var scene, version, value any
			e = tx.QueryRowContext(ctx, "SELECT scene_id,version,value FROM memory_semantic_candidates WHERE id=?", get(v, "id")).Scan(&scene, &version, &value)
			if errors.Is(e, sql.ErrNoRows) {
				return errors.New("recovery_semantic_delete_incomplete")
			}
			if e != nil {
				return e
			}
			if scene != get(v, "sceneId") || version != int64(get(v, "previousVersion").(float64)+1) || value != nil {
				return errors.New("recovery_semantic_delete_incomplete")
			}
		} else {
			current, e := s.Get(str(get(v, "id")))
			if e != nil {
				return e
			}
			if current == nil {
				return errors.New("recovery_semantic_material_missing")
			}
			h, e := semantic.Hash(current)
			if e != nil {
				return e
			}
			if get(current, "sceneId") != get(v, "sceneId") || get(current, "version") != get(v, "nextVersion") || h != get(v, "afterHash") {
				return errors.New("recovery_semantic_head_mismatch")
			}
		}
	}
	deleted, e := terminals(events)
	if e != nil {
		return e
	}
	rows, e := s.Rows(nil)
	if e != nil {
		return e
	}
	for _, candidate := range rows {
		for _, field := range []string{"parents", "anchorDependencies"} {
			for _, parent := range array(get(candidate, field)) {
				if deleted[str(get(obj(get(obj(parent), "ref")), "id"))] {
					return errors.New("recovery_semantic_privacy_incomplete")
				}
				for _, proof := range array(get(obj(parent), "proofs")) {
					for _, id := range array(get(obj(proof), "parents")) {
						if deleted[str(id)] {
							return errors.New("recovery_semantic_privacy_incomplete")
						}
					}
					for _, source := range array(get(obj(proof), "sources")) {
						yes, e := recalled(ctx, tx, str(get(obj(source), "sceneId")), str(get(obj(source), "messageId")))
						if e != nil {
							return e
						}
						if yes {
							return errors.New("recovery_semantic_privacy_incomplete")
						}
					}
				}
			}
		}
	}
	return nil
}
