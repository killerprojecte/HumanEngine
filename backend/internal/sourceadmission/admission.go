package sourceadmission

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// Admission cannot be assembled with independent or permissive authority hooks.
// The caller supplies the active child Reader, including its actual ownership
// and lifetime checks. SQL and readiness errors are never semantic rejections.
type Admission struct {
	reader    *assertions.Reader
	authority *assertionauthority.Authority
}

func New(reader *assertions.Reader, options assertionauthority.Options) (*Admission, error) {
	if reader == nil {
		return nil, errors.New("source_admission_reader_required")
	}
	a, e := assertionauthority.New(reader, options)
	if e != nil {
		return nil, e
	}
	return &Admission{reader, a}, nil
}

type Result struct {
	Status                               string
	Reason                               string
	Candidate, Scope, Proof, Attribution *Object
	Parents                              []any
}

func (a *Admission) Prepare(ctx, c *Object) (Result, error) {
	reject := func(reason string) (Result, error) { return Result{Status: "rejected", Reason: reason}, nil }
	if e := a.reader.CheckScope(); e != nil {
		return Result{}, e
	}
	scene, e := a.reader.Scene(str(get(ctx, "sceneId")))
	if e != nil {
		return Result{}, e
	}
	if !truthy(get(scene, "learningEnabled")) {
		return reject("learning_disabled")
	}
	sources := arr(get(c, "sources"))
	if len(sources) == 0 {
		return reject("missing_source_evidence")
	}
	// Validate all sources before rewriting any reference. Selection intentionally
	// uses the first identity match, even if a later duplicate was available.
	for _, s := range sources {
		if get(s, "sceneId") != get(ctx, "sceneId") {
			return reject("invalid_source_evidence")
		}
		recalled, e := a.reader.Recalled(str(get(s, "sceneId")), str(get(s, "messageId")))
		if e != nil {
			return Result{}, e
		}
		if recalled {
			return reject("invalid_source_evidence")
		}
		found := false
		for _, evidence := range arr(get(ctx, "sourceEvidence")) {
			if sameSource(s, evidence) && truthy(get(evidence, "available")) && !truthy(get(evidence, "isBot")) {
				found = true
				break
			}
		}
		if !found {
			return reject("invalid_source_evidence")
		}
	}
	normalized := []any{}
	for _, s := range sources {
		n := obj("sceneId", get(s, "sceneId"), "messageId", get(s, "messageId"), "senderId", get(s, "senderId"))
		for _, evidence := range arr(get(ctx, "sourceEvidence")) {
			if sameSource(s, evidence) {
				if truthy(get(evidence, "eventRef")) {
					_ = n.Set("eventRef", clone(get(evidence, "eventRef")))
				}
				break
			}
		}
		normalized = append(normalized, n)
	}
	candidate := clone(c).(*Object)
	_ = candidate.Set("sources", normalized)
	for _, s := range normalized {
		ok, e := a.authority.DomainSourceCurrent(object(s))
		if e != nil {
			return Result{}, e
		}
		if !ok {
			return reject("source_event_changed")
		}
	}
	attribution := ResolveSubject(ctx, candidate)
	if get(attribution, "ok") != true {
		return reject(str(get(attribution, "reason")))
	}
	// JS spread followed by undefined assignments removes the serialized field.
	for _, key := range []string{"personId", "kind", "epistemicKind"} {
		candidate = replaceOptional(candidate, key, attribution)
	}
	scope := obj("kind", "scene", "sceneId", get(ctx, "sceneId"))
	if truthy(get(candidate, "personId")) {
		_ = scope.Set("personId", get(candidate, "personId"))
	}
	parents := append([]any{}, arr(get(ctx, "dependencyMemoryIds"))...)
	refs := get(ctx, "dependencyParentReferences")
	if refs == nil {
		refs = []any{}
	}
	unique := []any{}
	seen := map[string]bool{}
	for _, s := range normalized {
		k, e := jsjson.Stringify([]any{get(s, "sceneId"), get(s, "messageId"), get(s, "senderId"), get(s, "eventRef")})
		if e != nil {
			return Result{}, e
		}
		if !seen[k] {
			seen[k] = true
			unique = append(unique, clone(s))
		}
	}
	proof := obj("sources", unique, "parents", parents, "parentReferences", clone(refs))
	if has(attribution, "witness") {
		_ = proof.Set("subjectWitness", clone(get(attribution, "witness")))
	}
	_ = proof.Set("sourceObservations", clone(get(attribution, "sourceObservations")))
	for _, s := range unique {
		if has(s, "eventRef") {
			if _, e := sourceevents.ParseRef(get(s, "eventRef")); e != nil {
				return reject("invalid_memory_dependencies")
			}
		}
	}
	provenance := get(ctx, "provenance")
	if assertionauthority.ParentBindingState(proof) != "bound" || (provenance == "source-only" && (len(parents) > 0 || len(arr(refs)) > 0)) {
		return reject("invalid_memory_dependencies")
	}
	if !truthy(provenance) || (provenance == "contextual" && !truthy(get(ctx, "dependencyMemoryIds"))) || (provenance == "source-only" && len(parents) > 0) {
		return reject("invalid_memory_dependencies")
	}
	for _, id := range parents {
		parent, e := a.reader.Record(str(id))
		if e != nil {
			return Result{}, e
		}
		if parent == nil {
			return reject("invalid_memory_dependencies")
		}
		ok, e := a.authority.ValidParent(scope, str(id))
		if e != nil {
			return Result{}, e
		}
		if !ok {
			return reject("invalid_memory_dependencies")
		}
		ok, e = a.authority.CurrentApplicable(parent)
		if e != nil {
			return Result{}, e
		}
		if !ok {
			return reject("invalid_memory_dependencies")
		}
	}
	ok, e := a.authority.ProofParentsAvailable(scope, proof, "current")
	if e != nil {
		return Result{}, e
	}
	if !ok {
		return reject("invalid_memory_dependencies")
	}
	return Result{Status: "accepted", Candidate: candidate, Scope: scope, Proof: proof, Attribution: attribution, Parents: parents}, nil
}
func replaceOptional(dst *Object, key string, src *Object) *Object {
	out := new(Object)
	present := false
	for _, m := range dst.Members() {
		if m.Key == key {
			present = true
			if has(src, key) {
				_ = out.Set(key, clone(get(src, key)))
			}
		} else {
			_ = out.Set(m.Key, m.Value)
		}
	}
	if !present && has(src, key) {
		_ = out.Set(key, clone(get(src, key)))
	}
	return out
}
