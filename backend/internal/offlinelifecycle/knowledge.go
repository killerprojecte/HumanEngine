package offlinelifecycle

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type knowledgeReplay struct{ owner *recovery.Maintenance }

func (p *knowledgeReplay) supported(ctx context.Context, tx *recovery.MaintenanceTx) ([]*object, error) {
	n, e := schemas(ctx, tx, "memory_knowledge_%", map[string][]string{"memory_knowledge_edges": {"admission_key", "assertion_id", "id", "scene_id", "value"}}, false, "recovery_unknown_knowledge_schema")
	if e != nil {
		return nil, e
	}
	if n == 0 {
		return []*object{}, nil
	}
	if n != 1 {
		return nil, errors.New("recovery_unknown_knowledge_schema")
	}
	s, e := knowledge.NewMaintenanceStore(p.owner, tx, ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.Rows(nil, nil)
	if e != nil {
		return nil, errors.New("recovery_knowledge_material_invalid")
	}
	for _, edge := range rows {
		_, has := edge.Get("property")
		if (get(edge, "predicate") == "has_property") != has {
			return nil, errors.New("recovery_knowledge_material_invalid")
		}
		if e = knowledge.ValidatePredicate(str(get(edge, "predicate")), obj(get(edge, "subject")), obj(get(edge, "object")), get(edge, "property")); e != nil {
			return nil, errors.New("recovery_knowledge_material_invalid")
		}
	}
	return rows, nil
}
func (p *knowledgeReplay) AssertSupported(ctx context.Context) error {
	return p.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { _, e := p.supported(ctx, tx); return e })
}
func (p *knowledgeReplay) Verify(ctx context.Context, tx *recovery.MaintenanceTx, events []string) error {
	rows, e := p.supported(ctx, tx)
	if e != nil {
		return e
	}
	deleted, e := terminals(events)
	if e != nil {
		return e
	}
	byID := map[string]*object{}
	for _, edge := range rows {
		byID[str(get(edge, "id"))] = edge
	}
	for _, edge := range rows {
		proofs := append([]any{}, array(get(edge, "proofs"))...)
		refs := []any{get(edge, "backing")}
		for _, d := range array(get(edge, "dependencies")) {
			proofs = append(proofs, array(get(obj(d), "proofs"))...)
			refs = append(refs, get(obj(get(obj(d), "entity")), "backing"))
		}
		for _, d := range array(get(edge, "readDependencies")) {
			proofs = append(proofs, array(get(obj(d), "proofs"))...)
			refs = append(refs, get(obj(d), "backing"))
		}
		for _, proof := range proofs {
			refs = append(refs, array(get(obj(proof), "parentReferences"))...)
			for _, id := range array(get(obj(proof), "parents")) {
				if deleted[str(id)] {
					return errors.New("recovery_knowledge_purge_incomplete")
				}
			}
			for _, source := range array(get(obj(proof), "sources")) {
				yes, e := recalled(ctx, tx, str(get(obj(source), "sceneId")), str(get(obj(source), "messageId")))
				if e != nil {
					return e
				}
				if yes {
					return errors.New("recovery_knowledge_purge_incomplete")
				}
			}
		}
		for _, ref := range refs {
			if deleted[str(get(obj(ref), "id"))] {
				return errors.New("recovery_knowledge_purge_incomplete")
			}
		}
		for _, span := range array(get(edge, "spans")) {
			source := obj(get(obj(span), "source"))
			yes, e := recalled(ctx, tx, str(get(source, "sceneId")), str(get(source, "messageId")))
			if e != nil {
				return e
			}
			if yes {
				return errors.New("recovery_knowledge_purge_incomplete")
			}
		}
		for _, d := range array(get(edge, "readDependencies")) {
			id := str(get(obj(d), "edgeId"))
			if id == "" {
				continue
			}
			origin := byID[id]
			if origin == nil || get(origin, "sceneId") != get(edge, "sceneId") || get(obj(get(origin, "backing")), "id") != get(obj(get(obj(d), "backing")), "id") {
				return errors.New("recovery_knowledge_dependency_missing")
			}
		}
		for _, d := range array(get(edge, "dependencies")) {
			origin := byID[str(get(obj(get(obj(d), "entity")), "originEdgeId"))]
			if origin == nil || get(origin, "sceneId") != get(edge, "sceneId") {
				return errors.New("recovery_knowledge_dependency_missing")
			}
		}
	}
	return nil
}
