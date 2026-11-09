package memoryservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
)

// semanticSnapshot emits the source's model-only projection. Private candidate
// fingerprints, dependency keys, roots and proof IDs remain in the captured raw
// page for validity but are not promoted to model authority.
func semanticSnapshot(scene string, raw *Object) (*Object, error) {
	invalid := errors.New("semantic_exposure_page_invalid")
	items, ok := value(raw, "items").([]any)
	if !ok || len(items) > 12 {
		return nil, invalid
	}
	coverage := text(raw, "coverage")
	if coverage != "complete" && coverage != "limited" {
		return nil, invalid
	}
	out := []any{}
	copyKeys := func(src *Object, keys []string) *Object {
		dst := new(Object)
		for _, key := range keys {
			if v, ok := src.Get(key); ok {
				_ = dst.Set(key, exposureClone(v))
			}
		}
		return dst
	}
	recordKeys := []string{"id", "scope", "content", "kind", "epistemicKind", "status", "origin", "learnedAt", "observedAt", "occurredAt", "validFrom", "validUntil", "internalUse", "lineageStatus", "parentBinding", "supersedesId", "sources"}
	for _, rawView := range items {
		view := object(rawView)
		candidate := object(value(view, "candidate"))
		if candidate == nil {
			return nil, invalid
		}
		item := copyKeys(candidate, []string{"id", "version", "personId", "kind", "text", "selectedParentIds", "status", "admission", "hypothesis", "anchorQualifications", "createdAt", "applicableUntil", "episodeAnchor"})
		kind := text(candidate, "kind")
		interpretation := "grouped_declarations_not_observed_event"
		if kind == "summary_candidate" {
			if text(candidate, "status") != "admitted" {
				return nil, invalid
			}
			interpretation = "attributed_summary_not_verified_truth"
		} else if kind == "duplicate_candidate" {
			interpretation = "possible_duplicate_not_merged"
		} else if kind != "episode_candidate" {
			return nil, invalid
		}
		if text(view, "interpretation") != interpretation {
			return nil, invalid
		}
		parents := []any{}
		refs := map[string]*Object{}
		for _, rawParent := range epArr(value(candidate, "parents")) {
			parent := object(rawParent)
			ref := object(value(parent, "ref"))
			if ref == nil || refs[text(ref, "id")] != nil {
				return nil, invalid
			}
			refs[text(ref, "id")] = ref
			proofs := []any{}
			for _, p := range epArr(value(parent, "proofs")) {
				proof := object(p)
				if proof == nil {
					return nil, invalid
				}
				proofs = append(proofs, copyKeys(proof, []string{"sources", "parents", "parentReferences", "subjectWitness", "sourceObservations"}))
			}
			if len(proofs) < 1 || len(proofs) > 128 {
				return nil, invalid
			}
			parents = append(parents, epObj("ref", exposureClone(ref), "proofs", proofs))
		}
		if len(parents) < 2 || len(parents) > 12 {
			return nil, invalid
		}
		declarations := []any{}
		for _, rawDeclaration := range epArr(value(view, "declarations")) {
			d := object(rawDeclaration)
			ref := object(value(d, "ref"))
			record := object(value(d, "record"))
			if ref == nil || record == nil || text(ref, "id") != text(record, "id") || !deepEqual(refs[text(ref, "id")], ref) {
				return nil, invalid
			}
			scope := object(value(record, "scope"))
			if text(scope, "kind") != "scene" || text(scope, "sceneId") != scene {
				return nil, errors.New("semantic_exposure_scope")
			}
			for _, source := range epArr(value(record, "sources")) {
				if text(object(source), "sceneId") != scene {
					return nil, errors.New("semantic_exposure_scope")
				}
			}
			declarations = append(declarations, epObj("ref", exposureClone(ref), "record", copyKeys(record, recordKeys)))
		}
		if len(declarations) != len(parents) {
			return nil, invalid
		}
		for _, id := range epArr(value(candidate, "selectedParentIds")) {
			s, ok := id.(string)
			if !ok || refs[s] == nil {
				return nil, invalid
			}
		}
		for _, parent := range parents {
			for _, proof := range epArr(epGet(parent, "proofs")) {
				for _, source := range epArr(epGet(proof, "sources")) {
					if text(object(source), "sceneId") != scene {
						return nil, errors.New("semantic_exposure_scope")
					}
				}
			}
		}
		_ = item.Set("parents", parents)
		_ = item.Set("declarations", declarations)
		_ = item.Set("rootCount", float64(len(epArr(value(view, "roots")))))
		_ = item.Set("applicability", value(view, "applicability"))
		_ = item.Set("interpretation", interpretation)
		out = append(out, item)
	}
	return semantic.ValidateModelEvidence(epObj("stateKind", "semantic_view", "page", epObj("coverage", coverage, "sourceBodyCoverage", "unavailable", "items", out)))
}
func semanticSourceIDs(raw *Object, values []*Object) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	sources := func(v any) error {
		for _, raw := range epArr(epGet(v, "sources")) {
			id, ok := epGet(raw, "messageId").(string)
			if !ok {
				return errors.New("state_exposure_source_invalid")
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		return nil
	}
	proofs := func(v any) error {
		for _, p := range epArr(epGet(v, "proofs")) {
			if e := sources(p); e != nil {
				return e
			}
		}
		return nil
	}
	for _, view := range epArr(value(raw, "items")) {
		for _, dep := range epArr(epGet(epGet(view, "candidate"), "anchorDependencies")) {
			if e := proofs(dep); e != nil {
				return nil, e
			}
		}
	}
	for _, v := range values {
		for _, item := range epArr(epGet(value(v, "page"), "items")) {
			for _, d := range epArr(epGet(item, "declarations")) {
				if e := sources(epGet(d, "record")); e != nil {
					return nil, e
				}
			}
			for _, p := range epArr(epGet(item, "parents")) {
				if e := proofs(p); e != nil {
					return nil, e
				}
			}
		}
	}
	return out, nil
}
