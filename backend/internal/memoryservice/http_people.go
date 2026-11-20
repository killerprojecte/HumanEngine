package memoryservice

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryread"
	"github.com/killerprojecte/HumanEngine/backend/internal/profileidentity"
)

type PeopleQuery struct{ SceneID, PersonID string }

func realPeopleScene(r *assertions.Reader, sceneID string) (bool, error) {
	scene, err := r.Scene(sceneID)
	kind := text(scene, "kind")
	return kind == "group" || kind == "private", err
}

// PublishPeople refreshes the existing support lifecycle only for a real scene,
// then projects the scoped rows, evidence and names under one publication.
func (a *AdminRead) PublishPeople(ctx context.Context, q *PeopleQuery, emit func(int, []byte) error) (bool, error) {
	status := http.StatusOK
	if q != nil {
		var real bool
		if err := a.memory.d.Assertions.Read(ctx, func(r *assertions.Reader) (err error) {
			real, err = realPeopleScene(r, q.SceneID)
			return err
		}); err != nil {
			return false, err
		}
		if !real {
			// No support refresh is needed for a missing or non-real scene.
			status = http.StatusNotFound
		} else if err := a.memory.RefreshSupport(ctx); err != nil {
			return false, err
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

		read, err := memoryread.New(r, a.memory.d.Authority)
		if err != nil {
			return nil, err
		}
		input := memoryread.Input{SceneID: q.SceneID, PersonID: q.PersonID}
		names := peopleNames{r, q.SceneID, map[string]string{}}
		compare := a.memory.d.Authority.Compare
		if q.PersonID == "" {
			records, err := read.List(input)
			if err != nil {
				return nil, err
			}
			return peopleDirectory(q.SceneID, records, &names, compare)
		}
		evidence := map[*Object][]any{}
		records, err := read.ListWithProofs(input, func(record *Object, proofs []any) {
			if len(evidence) < 200 && text(peopleScope(record, q.SceneID), "personId") == q.PersonID {
				evidence[record] = proofs
			}
		})
		if err != nil {
			return nil, err
		}
		return peopleProfile(*q, records, evidence, &names, compare)
	}, func(body []byte) error { return emit(status, body) })
}

type peopleNames struct {
	reader *assertions.Reader
	scene  string
	names  map[string]string
}

func (n *peopleNames) set(o *Object, key, person string) error {
	name, seen := n.names[person]
	if !seen {
		profile, err := n.reader.PersonPresentation(n.scene, person)
		if err != nil {
			return err
		}
		name, _, err = profileidentity.DisplayName(value(object(profile), "senderDisplayName"))
		if err != nil {
			return err
		}
		n.names[person] = name
	}
	if name != "" {
		_ = o.Set(key, name)
	}
	return nil
}

func peopleScope(record *Object, scene string) *Object {
	scope := object(value(record, "scope"))
	if text(scope, "kind") == "scene" && text(scope, "sceneId") == scene {
		return scope
	}
	return nil
}

func peopleDirectory(scene string, records []*Object, names *peopleNames, compare func(string, string) (int, error)) ([]byte, error) {
	ids := []string{}
	counts := map[string]int{}
	for _, record := range records {
		if id := text(peopleScope(record, scene), "personId"); id != "" {
			if counts[id] == 0 {
				ids = append(ids, id)
			}
			counts[id]++
		}
	}
	var orderErr error
	slices.SortStableFunc(ids, func(a, b string) int {
		if orderErr != nil {
			return 0
		}
		order, err := compare(a, b)
		orderErr = err
		return order
	})
	if orderErr != nil {
		return nil, orderErr
	}
	people := make([]any, 0, len(ids))
	for _, id := range ids {
		person := personObject("personId", id, "recordCount", float64(counts[id]))
		if err := names.set(person, "displayName", id); err != nil {
			return nil, err
		}
		people = append(people, person)
	}
	return jsjson.Marshal(personObject("sceneId", scene, "people", people))
}

var errPersonRecord = errors.New("person_memory_record_invalid")

// personFields applies the closed source profile response schema. Stored proof
// properties cannot escape through a field of the wrong shape.
func personFields(from *Object, required, optional []string) (*Object, error) {
	if from == nil {
		return nil, errPersonRecord
	}
	out := personObject()
	for _, key := range required {
		v, _ := from.Get(key)
		if !personFieldValid(key, v) {
			return nil, errPersonRecord
		}
		_ = out.Set(key, v)
	}
	for _, key := range optional {
		if v, exists := from.Get(key); exists && !jsjson.IsUndefined(v) {
			if !personFieldValid(key, v) {
				return nil, errPersonRecord
			}
			_ = out.Set(key, v)
		}
	}
	return out, nil
}

func personFieldValid(key string, v any) bool {
	switch key {
	case "confidence":
		n, ok := v.(float64)
		return ok && !math.IsNaN(n) && !math.IsInf(n, 0)
	case "mentionPartIndex":
		n, ok := v.(float64)
		return ok && n >= 0 && n <= 9007199254740991 && math.Trunc(n) == n
	case "locked":
		_, ok := v.(bool)
		return ok
	case "status":
		return v == "active" || v == "disputed" || v == "superseded" || v == "invalidated"
	case "origin":
		return v == "manual" || v == "learned"
	case "importance":
		return v == "low" || v == "medium" || v == "high"
	case "epistemicKind":
		return v == "explicit" || v == "reported" || v == "observed" || v == "inferred" || v == "hypothesis"
	case "lineageStatus":
		return v == "verified" || v == "unverified" || v == "quarantined"
	case "kind":
		return v == "fact" || v == "claim" || v == "term" || v == "experience" || v == "summary"
	default:
		_, ok := v.(string)
		return ok
	}
}

func personReferences(values []any, names *peopleNames, observations bool) ([]any, error) {
	refs := make([]any, 0, len(values))
	for _, raw := range values {
		source := object(raw)
		var optional []string
		if observations {
			optional = []string{"sentAt", "receivedAt"}
		}
		ref, err := personFields(source, []string{"sceneId", "messageId", "senderId"}, optional)
		if err != nil {
			return nil, err
		}
		if names != nil && text(source, "sceneId") == names.scene {
			if err := names.set(ref, "senderDisplayName", text(source, "senderId")); err != nil {
				return nil, err
			}
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func peopleProfile(q PeopleQuery, visible []*Object, evidence map[*Object][]any, names *peopleNames, compare func(string, string) (int, error)) ([]byte, error) {
	records, timeline := []any{}, []any{}
	coverage := "complete"
	for _, entry := range visible {
		scope := peopleScope(entry, q.SceneID)
		if text(scope, "personId") != q.PersonID {
			continue
		}
		record, err := personFields(entry, []string{"id", "kind", "content", "importance", "confidence", "learnedAt", "status", "origin", "locked"}, []string{"observedAt", "validFrom", "epistemicKind", "occurredAt", "validUntil", "supersedesId", "lineageStatus"})
		if err != nil {
			return nil, err
		}
		sources, ok := value(entry, "sources").([]any)
		if !ok {
			return nil, errPersonRecord
		}
		refs, err := personReferences(sources, nil, false)
		if err != nil {
			return nil, err
		}
		_ = record.Set("scope", personObject("kind", "scene", "sceneId", q.SceneID, "personId", q.PersonID))
		_ = record.Set("sources", refs)
		id := text(record, "id")
		if proofs, ok := evidence[entry]; ok {
			evidence, err := peopleEvidence(entry, proofs, names)
			if err != nil {
				return nil, err
			}
			_ = record.Set("subjectEvidence", evidence)
		} else {
			coverage = "limited"
		}
		records = append(records, record)
		item := personObject("memoryId", id, "recordedAt", value(record, "learnedAt"), "status", value(record, "status"), "origin", value(record, "origin"), "sourceCount", float64(len(refs)))
		for _, key := range []string{"observedAt", "validFrom", "occurredAt", "validUntil"} {
			if v := value(record, key); truthy(v) {
				_ = item.Set(key, v)
			}
		}
		timeline = append(timeline, item)
	}
	var err error
	slices.SortStableFunc(timeline, func(aRaw, bRaw any) int {
		a, b := aRaw.(*Object), bRaw.(*Object)
		if err != nil {
			return 0
		}
		var cmp int
		cmp, err = compare(text(a, "recordedAt"), text(b, "recordedAt"))
		if err == nil && cmp == 0 {
			cmp, err = compare(text(a, "memoryId"), text(b, "memoryId"))
		}
		return cmp
	})
	if err != nil {
		return nil, err
	}
	out := personObject("sceneId", q.SceneID, "personId", q.PersonID, "records", records, "timeline", timeline, "subjectEvidenceCoverage", coverage)
	if len(records) != 0 {
		if err := names.set(out, "displayName", q.PersonID); err != nil {
			return nil, err
		}
	}
	return jsjson.Marshal(out)
}

func peopleEvidence(record *Object, current []any, names *peopleNames) (*Object, error) {
	coverage := "complete"
	if len(current) > 40 {
		coverage = "limited"
	}
	witnesses := []any{}
	for _, raw := range current[:min(len(current), 40)] {
		proof := object(raw)
		witness := object(value(proof, "subjectWitness"))
		if witness == nil {
			continue
		}
		view, err := personFields(witness, nil, []string{"personId", "sourceMessageId", "mentionPartIndex"})
		if err != nil {
			return nil, err
		}
		kind := value(witness, "kind")
		if kind != "self" && kind != "mentioned" && kind != "legacy_sender" && kind != "unresolved" {
			return nil, errPersonRecord
		}
		_ = view.Set("kind", kind)
		if id := text(witness, "personId"); id != "" {
			if err := names.set(view, "subjectDisplayName", id); err != nil {
				return nil, err
			}
		}
		sources, _ := value(proof, "sources").([]any)
		observations, _ := value(proof, "sourceObservations").([]any)
		refs, err := personReferences(sources, names, false)
		if err != nil {
			return nil, err
		}
		obs, err := personReferences(observations, nil, true)
		if err != nil {
			return nil, err
		}
		_ = view.Set("sources", refs)
		_ = view.Set("observations", obs)
		witnesses = append(witnesses, view)
	}
	out := personObject("coverage", coverage, "witnesses", witnesses)
	if at, ok := record.Get("observedAt"); ok {
		_ = out.Set("observedAt", at)
	}
	return out, nil
}

func personObject(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
