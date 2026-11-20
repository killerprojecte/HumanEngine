package memoryservice

import (
	"context"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryread"
	"github.com/killerprojecte/HumanEngine/backend/internal/relations"
)

type RelationsQuery struct {
	SceneID string
	List    relations.ListOptions
}

// PublishRelations keeps the lifecycle write used by memory.list outside the
// assertion reader. The final publication rechecks scene admission and joins
// current graph evidence, assertion content and scene-local names in one scope.
func (a *AdminRead) PublishRelations(ctx context.Context, q *RelationsQuery, schemas *contract.Schemas, emit func(int, []byte) error) (bool, error) {
	status := http.StatusOK
	if q != nil {
		var real, admitted bool
		err := a.memory.d.Assertions.Read(ctx, func(r *assertions.Reader) (err error) {
			admitted = true
			real, err = realPeopleScene(r, q.SceneID)
			return err
		})
		if err != nil {
			return admitted, err
		}
		if !real {
			status = http.StatusNotFound
		} else if err := a.memory.RefreshSupport(ctx); err != nil {
			return true, err
		}
	}
	return a.publishBody(ctx, func(r *assertions.Reader) ([]byte, error) {
		if q == nil {
			status = http.StatusBadRequest
			return []byte(`{"error":"Invalid request"}`), nil
		}
		if status == http.StatusOK {
			real, err := realPeopleScene(r, q.SceneID)
			if err != nil {
				return nil, err
			}
			if !real {
				status = http.StatusNotFound
			}
		}
		if status == http.StatusNotFound {
			return []byte(`{"error":"Scene unavailable"}`), nil
		}

		raw, exists, err := r.SettingsInput()
		if err != nil {
			return nil, err
		}
		input := []byte(raw)
		if !exists {
			input = contract.DefaultSettingsInput()
		}
		settings, err := schemas.Parse("Settings", input)
		if err != nil {
			return nil, err
		}
		// The admin view uses the graph flag, not the independent model answer flag.
		enabled := settings["memoryGraphEnabled"] == true
		options := relations.Options{
			Authority: a.memory.d.Authority,
			Enabled:   func(relations.Context) (bool, error) { return enabled, nil },
		}
		result, err := relations.ListInReadScope(r, options, relations.Context{SceneID: q.SceneID, Provenance: "contextual"}, q.List)
		if err != nil {
			return nil, err
		}
		items := []any{}
		if len(result.Items) != 0 {
			items, err = a.relationAdminItems(r, q.SceneID, result.Items, schemas)
			if err != nil {
				return nil, err
			}
		}
		return jsjson.Marshal(personObject("sceneId", q.SceneID, "enabled", enabled, "items", items, "coverage", result.Coverage))
	}, func(body []byte) error { return emit(status, body) })
}

func (a *AdminRead) relationAdminItems(r *assertions.Reader, sceneID string, rows []*Object, schemas *contract.Schemas) ([]any, error) {
	// The fixed list reader applies the same scene, support, and applicability
	// checks as memory.list. Request only IDs already qualified by the graph;
	// the IDs narrow the SQL scan but cannot authorize assertion content.
	backingIDs := make([]string, len(rows))
	for i, row := range rows {
		backingIDs[i] = text(object(value(row, "backing")), "id")
	}
	read, err := memoryread.New(r, a.memory.d.Authority)
	if err != nil {
		return nil, err
	}
	records, err := read.List(memoryread.Input{SceneID: sceneID, IDs: backingIDs})
	if err != nil {
		return nil, err
	}
	contents := make(map[string]any, len(records))
	for _, record := range records {
		contents[text(record, "id")] = value(record, "content")
	}
	items := make([]any, 0, len(rows))
	names := peopleNames{r, sceneID, map[string]string{}}
	for i, row := range rows {
		content, found := contents[backingIDs[i]]
		if !found {
			continue
		}
		view, err := relationAdminView(row, content, &names)
		if err != nil {
			return nil, err
		}
		if err := schemas.ValidateMemoryRelationView(view); err != nil {
			return nil, err
		}
		items = append(items, view)
	}
	return items, nil
}

// relationAdminView deliberately never copies internal graph or proof objects,
// even when persisted data also contains fields not present in the public schema.
func relationAdminView(row *Object, content any, names *peopleNames) (*Object, error) {
	view := personObject("id", value(row, "id"), "version", value(row, "version"),
		"assertionContent", content, "personId", value(row, "personId"))
	if err := names.set(view, "personName", text(row, "personId")); err != nil {
		return nil, err
	}
	target := object(value(row, "target"))
	_ = view.Set("predicate", value(row, "predicate"))
	_ = view.Set("target", personObject("id", value(target, "id"), "kind", value(target, "kind"), "title", value(target, "title")))
	for _, key := range []string{"polarity", "modality", "temporalScope", "conditions", "occurredAt", "validFrom", "validUntil", "epistemicKind", "status", "admission", "autoReason", "createdAt", "reviewedAt"} {
		if v, ok := row.Get(key); ok {
			_ = view.Set(key, v)
		}
	}
	proofs, _ := value(row, "proofs").([]any)
	groups := make([]any, 0, len(proofs))
	for _, raw := range proofs {
		proof := object(raw)
		group := personObject()
		if witness := object(value(proof, "subjectWitness")); witness != nil {
			if v, ok := witness.Get("kind"); ok {
				_ = group.Set("witnessKind", v)
			}
		}
		refs, _ := value(proof, "sources").([]any)
		sources := make([]any, 0, len(refs))
		for _, raw := range refs {
			source := object(raw)
			item := personObject("messageId", value(source, "messageId"), "senderId", value(source, "senderId"))
			if text(source, "sceneId") == names.scene {
				if err := names.set(item, "senderName", text(source, "senderId")); err != nil {
					return nil, err
				}
			}
			sources = append(sources, item)
		}
		parents, _ := value(proof, "parents").([]any)
		_ = group.Set("sources", sources)
		_ = group.Set("parentCount", float64(len(parents)))
		groups = append(groups, group)
	}
	_ = view.Set("sourceGroups", groups)
	return view, nil
}
