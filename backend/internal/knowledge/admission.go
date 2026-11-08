package knowledge

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

// SpanAuthority supplies the source facade's exact event and UTF-16 validation.
// Production composition binds sourceadmission.Admission to the same child as
// this Reader and Store. It does not grant authority to construct a Reader.
type SpanAuthority interface {
	KnowledgeSpans(*Object, *Object, []any) ([]any, error)
}
type AdmissionPolicy struct {
	LearningEnabled bool
	AutoAdmission   bool
	// ID is optional. Production defaults to cryptographically random UUID v4.
	ID func() string
}
type AdmissionRejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}
type AdmissionResult struct {
	Created  []*Object            `json:"created"`
	Rejected []AdmissionRejection `json:"rejected"`
}

func admissionReject(index int, reason string) AdmissionResult {
	return AdmissionResult{Created: []*Object{}, Rejected: []AdmissionRejection{{index, reason}}}
}

type admissionSemantic string

func (e admissionSemantic) Error() string { return string(e) }
func admissionUUID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Admit plans every input before the first Put. Semantic failure is returned in
// Rejected, including caught planning errors exactly as in the source. Final
// Put errors are outside that catch and propagate to the outer transaction.
// ctx must be the original trusted context, before marker-parent rewriting.
func (r *Reader) Admit(storage *Store, source SpanAuthority, ctx, backing *Object, inputs []any, policy AdmissionPolicy) (AdmissionResult, error) {
	empty := AdmissionResult{Created: []*Object{}, Rejected: []AdmissionRejection{}}
	if r.storage != storage {
		return empty, errors.New("knowledge_admission_store_binding")
	}
	if e := storage.RequireTransaction(); e != nil {
		return empty, e
	}
	c := Context{SceneID: str(get(ctx, "sceneId")), Simulation: truthy(get(ctx, "simulation")), Provenance: str(get(ctx, "provenance")), ExposedEntities: arr(get(ctx, "exposedEntities"))}
	if c.Simulation {
		return admissionReject(-1, "knowledge_disabled"), nil
	}
	enabled, e := r.enabled(c)
	if e != nil {
		return empty, e
	}
	if c.Simulation || !enabled || !policy.LearningEnabled {
		return admissionReject(-1, "knowledge_disabled"), nil
	}
	if source == nil {
		return empty, errors.New("knowledge_source_authority_required")
	}
	if _, e := ref(backing); len(inputs) > 3 || e != nil {
		return admissionReject(-1, "knowledge_input_limit"), nil
	}
	reads := []any{}
	exposedAssertions, exposedEdges := arr(get(ctx, "exposedAssertions")), arr(get(ctx, "exposedEdges"))
	if len(exposedEdges) > 40 || len(exposedAssertions) > 128 || (c.Provenance == "source-only" && (len(exposedEdges) > 0 || len(exposedAssertions) > 0)) {
		return admissionReject(-1, "knowledge_read_dependencies_invalid"), nil
	}
	stage := newReadStage()
	for _, b := range exposedAssertions {
		if _, e := ref(b); e != nil {
			return admissionReject(-1, "knowledge_read_dependency_invalid"), nil
		}
		for _, d := range reads {
			if !truthy(get(d, "edgeId")) && get(get(d, "backing"), "id") == get(b, "id") {
				return admissionReject(-1, "knowledge_read_dependency_invalid"), nil
			}
		}
		view, e := r.Current(c, referenceValue(b))
		if e != nil {
			return admissionReject(-1, admissionReason(e, "knowledge_read_dependencies_invalid")), nil
		}
		if view == nil {
			return admissionReject(-1, "knowledge_read_dependency_unavailable"), nil
		}
		fp, e := BackingFingerprint(view.Record)
		if e != nil {
			return admissionReject(-1, admissionReason(e, "knowledge_read_dependencies_invalid")), nil
		}
		reads = append(reads, obj("backing", clone(b), "backingFingerprint", fp, "proofs", clone(view.Proofs)))
	}
	for _, snapshot := range exposedEdges {
		for _, d := range reads {
			if get(d, "edgeId") == get(snapshot, "id") {
				return admissionReject(-1, "knowledge_read_dependency_duplicate"), nil
			}
		}
		original, e := storage.Get(str(get(snapshot, "id")))
		if e != nil {
			return admissionReject(-1, admissionReason(e, "knowledge_read_dependencies_invalid")), nil
		}
		if original == nil || get(original, "version") != get(snapshot, "version") || !sameReadRef(get(original, "backing"), get(snapshot, "backing")) {
			return admissionReject(-1, "knowledge_read_dependency_stale"), nil
		}
		edge, e := r.project(c, original, map[string]bool{}, stage)
		if e != nil {
			if errors.Is(e, errDependencyLimit) {
				return admissionReject(-1, e.Error()), nil
			}
			return admissionReject(-1, admissionReason(e, "knowledge_read_dependencies_invalid")), nil
		}
		if edge == nil {
			return admissionReject(-1, "knowledge_read_dependency_unavailable"), nil
		}
		reads = append(reads, obj("edgeId", get(edge, "id"), "edgeVersion", get(edge, "version"), "backing", get(edge, "backing"), "backingFingerprint", get(edge, "backingFingerprint"), "proofs", clone(get(edge, "proofs"))))
	}
	type item struct {
		edge      *Object
		key       string
		duplicate bool
	}
	pending := []item{}
	id := policy.ID
	if id == nil {
		id = admissionUUID
	}
	for index, input := range inputs {
		plan := func() (item, error) {
			p, e := ParseProposal(input)
			if e != nil {
				return item{}, admissionSemantic("knowledge_proposal_invalid")
			}
			view, e := r.Current(c, referenceValue(backing))
			if e != nil {
				return item{}, e
			}
			if view == nil {
				return item{}, admissionSemantic("knowledge_backing_unavailable")
			}
			edgeID := id()
			dependencies := []any{}
			subject, e := r.admissionEndpoint(c, backing, edgeID, object(get(p, "subject")), &dependencies, id)
			if e != nil {
				return item{}, e
			}
			objectPoint, e := r.admissionEndpoint(c, backing, edgeID, object(get(p, "object")), &dependencies, id)
			if e != nil {
				return item{}, e
			}
			if e = ValidatePredicate(str(get(p, "predicate")), subject, objectPoint, get(p, "property")); e != nil {
				return item{}, admissionSemantic(e.Error())
			}
			spans, e := source.KnowledgeSpans(ctx, p, view.Proofs)
			if e != nil {
				return item{}, e
			}
			for _, span := range spans {
				stance := get(span, "stance")
				if (stance == "quoted" || stance == "reported") && (get(view.Record, "kind") != "claim" || !contains([]string{"reported", "inferred", "hypothesis"}, str(get(view.Record, "epistemicKind")))) {
					return item{}, admissionSemantic("knowledge_stance_backing_conflict")
				}
			}
			for _, span := range spans {
				if get(get(p, "subject"), "kind") != "self" && get(span, "stance") == "self" {
					return item{}, admissionSemantic("knowledge_self_stance_subject")
				}
			}
			proofs := []any{}
			for _, proof := range view.Proofs {
				found := false
				for _, s := range arr(get(proof, "sources")) {
					for _, span := range spans {
						if deepEqual(s, get(span, "source")) {
							found = true
						}
					}
				}
				if found {
					proofs = append(proofs, proof)
				}
			}
			if len(proofs) == 0 {
				return item{}, admissionSemantic("knowledge_proof_unavailable")
			}
			key, e := digest([]any{c.SceneID, backing, p, reads})
			if e != nil {
				return item{}, e
			}
			previous, e := storage.ByKey(key)
			if e != nil {
				return item{}, e
			}
			for _, q := range pending {
				if q.key == key {
					return item{}, admissionSemantic("knowledge_duplicate_proposal")
				}
			}
			if previous != nil {
				current, e := r.Project(c, previous)
				if e != nil {
					return item{}, e
				}
				if current == nil {
					return item{}, admissionSemantic("knowledge_duplicate_unavailable")
				}
				return item{current, key, true}, nil
			}
			fp, e := BackingFingerprint(view.Record)
			if e != nil {
				return item{}, e
			}
			edge := obj("id", edgeID, "version", float64(1), "sceneId", c.SceneID, "registryVersion", float64(1), "predicate", get(p, "predicate"))
			if truthy(get(p, "property")) {
				_ = edge.Set("property", get(p, "property"))
			}
			for _, kv := range []struct {
				k string
				v any
			}{{"subject", subject}, {"object", objectPoint}, {"polarity", get(p, "polarity")}, {"modality", get(p, "modality")}, {"temporalScope", get(p, "temporalScope")}, {"conditions", get(p, "conditions")}} {
				_ = edge.Set(kv.k, kv.v)
			}
			for _, k := range []string{"validFrom", "validUntil"} {
				if truthy(get(p, k)) {
					_ = edge.Set(k, get(p, k))
				}
			}
			for _, kv := range []struct {
				k string
				v any
			}{{"backing", backing}, {"backingFingerprint", fp}, {"proofs", clone(proofs)}, {"spans", spans}, {"dependencies", dependencies}, {"readDependencies", clone(reads)}, {"status", "candidate"}, {"admission", "qualified_candidate"}, {"admissionPolicyVersion", float64(1)}, {"createdAt", isoMillis(r.now())}} {
				_ = edge.Set(kv.k, kv.v)
			}
			limit := 201
			visible, e := storage.Rows(&c.SceneID, &limit)
			if e != nil {
				return item{}, e
			}
			for _, q := range pending {
				visible = append(visible, q.edge)
			}
			conflict := len(visible) > 200
			edgeKey, e := conflictKey(edge)
			if e != nil {
				return item{}, e
			}
			if !conflict {
				for _, v := range visible {
					vk, e := conflictKey(v)
					if e != nil {
						return item{}, e
					}
					if vk == edgeKey {
						projected, e := r.Project(c, v)
						if e != nil {
							return item{}, e
						}
						if projected != nil {
							conflict = true
							break
						}
					}
				}
			}
			selfPolicy := contains([]string{"participates_in", "prefers_topic", "uses_tool", "member_of"}, str(get(p, "predicate")))
			self := get(get(p, "subject"), "kind") == "self"
			eligible := !self && contains([]string{"explicit", "observed"}, str(get(view.Record, "epistemicKind")))
			stance := "asserted"
			if selfPolicy {
				eligible = self && get(view.Record, "epistemicKind") == "explicit"
				stance = "self"
			}
			for _, s := range spans {
				if get(s, "stance") != stance {
					eligible = false
				}
			}
			if !conflict && policy.AutoAdmission && eligible && get(p, "polarity") == "positive" && get(p, "modality") == "asserted" && get(p, "temporalScope") == "current" && len(arr(get(p, "conditions"))) == 0 {
				inTime := true
				if truthy(get(p, "validFrom")) {
					ms, ok, e := r.dates.ParseMillis(str(get(p, "validFrom")))
					if e != nil {
						return item{}, e
					}
					now := r.now()
					inTime = ok && ms <= now
				}
				if inTime && truthy(get(p, "validUntil")) {
					now := r.now()
					ms, ok, e := r.dates.ParseMillis(str(get(p, "validUntil")))
					if e != nil {
						return item{}, e
					}
					inTime = ok && now < ms
				}
				if inTime && len(dependencies) == 0 && len(reads) == 0 {
					_ = edge.Set("status", "accepted")
					admission := "direct_assertion_no_visible_conflict"
					if selfPolicy {
						admission = "self_explicit_no_visible_conflict"
					}
					_ = edge.Set("admission", admission)
				}
			}
			if e := ValidateEdge(edge); e != nil {
				return item{}, admissionSemantic("knowledge_proposal_invalid")
			}
			return item{edge, key, false}, nil
		}
		q, e := plan()
		if e != nil {
			return admissionReject(index, admissionReason(e, "knowledge_proposal_invalid")), nil
		}
		pending = append(pending, q)
	}
	for _, q := range pending {
		if !q.duplicate {
			if e := storage.Put(q.edge, q.key); e != nil {
				return empty, e
			}
		}
		empty.Created = append(empty.Created, q.edge)
	}
	return empty, nil
}
func (r *Reader) admissionEndpoint(c Context, backing *Object, edgeID string, input *Object, dependencies *[]any, id func() string) (*Object, error) {
	switch get(input, "kind") {
	case "literal":
		return clone(input).(*Object), nil
	case "self":
		view, e := r.Current(c, referenceValue(backing))
		if e != nil {
			return nil, e
		}
		verified := false
		if view != nil && get(get(view.Record, "scope"), "kind") == "scene" && get(get(view.Record, "scope"), "personId") == get(input, "personId") {
			for _, p := range view.Proofs {
				w := get(p, "subjectWitness")
				if get(w, "kind") == "self" && get(w, "personId") == get(input, "personId") {
					for _, s := range arr(get(p, "sources")) {
						if get(s, "senderId") == get(input, "personId") && get(s, "messageId") == get(w, "sourceMessageId") {
							verified = true
						}
					}
				}
			}
		}
		if !verified {
			return nil, admissionSemantic("knowledge_subject_unverified")
		}
		return obj("kind", "person", "personId", get(input, "personId")), nil
	case "new":
		return obj("kind", "entity", "entity", obj("id", id(), "sceneId", c.SceneID, "kind", get(input, "entityKind"), "title", get(input, "title"), "backing", backing, "originEdgeId", edgeID)), nil
	}
	if c.Provenance != "contextual" {
		return nil, admissionSemantic("knowledge_existing_source_only")
	}
	entity, e := r.storage.Entity(str(get(input, "entityId")), c.SceneID)
	if e != nil {
		return nil, e
	}
	exposed := false
	if entity != nil && get(entity, "sceneId") == c.SceneID {
		for _, x := range c.ExposedEntities {
			if get(x, "entityId") == get(entity, "id") && sameReadRef(get(x, "backingRef"), get(entity, "backing")) {
				exposed = true
			}
		}
	}
	if !exposed {
		return nil, admissionSemantic("knowledge_entity_not_exposed")
	}
	view, e := r.Current(c, referenceValue(get(entity, "backing")))
	if e != nil {
		return nil, e
	}
	origin, e := r.storage.Get(str(get(entity, "originEdgeId")))
	if e != nil {
		return nil, e
	}
	if view == nil || origin == nil {
		return nil, admissionSemantic("knowledge_entity_unavailable")
	}
	projected, e := r.Project(c, origin)
	if e != nil {
		return nil, e
	}
	if projected == nil {
		return nil, admissionSemantic("knowledge_entity_unavailable")
	}
	fp, e := BackingFingerprint(view.Record)
	if e != nil {
		return nil, e
	}
	*dependencies = append(*dependencies, obj("entity", clone(entity), "backingFingerprint", fp, "proofs", clone(view.Proofs)))
	return obj("kind", "entity", "entity", entity), nil
}

func admissionReason(e error, fallback string) string {
	if strings.HasPrefix(e.Error(), "knowledge_") {
		return e.Error()
	}
	return fallback
}
