package semantic

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func modelString(v any) (any, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	return nil, ErrContract
}
func modelNullableString(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	return modelString(v)
}
func modelText(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, ErrContract
	}
	n, e := jsjson.CodePointCount(s)
	if e != nil || n < 1 || n > 6000 {
		return nil, ErrContract
	}
	return s, nil
}
func modelStrip(v any, fields ...scField) (*jsjson.Object, error) {
	o := scObject(v)
	if o == nil {
		return nil, ErrContract
	}
	picked := new(jsjson.Object)
	for _, f := range fields {
		if x, ok := o.Get(f.name); ok {
			_ = picked.Set(f.name, x)
		}
	}
	return scShape(picked, fields...)
}
func modelSource(v any) (any, error) {
	return modelStrip(v, scF("sceneId", modelString), scF("messageId", modelString), scF("senderId", modelString), scOpt("eventRef", scEventRef))
}
func modelScope(v any) (any, error) {
	return modelStrip(v, scF("kind", scEnumeration("scene")), scF("sceneId", modelString), scOpt("personId", modelString))
}
func modelRecord(v any) (any, error) {
	fields := []scField{scF("id", modelString), scF("scope", modelScope), scF("content", modelString), scF("kind", modelString), scOpt("epistemicKind", modelString), scF("status", scEnumeration("active", "disputed")), scF("origin", modelString), scF("learnedAt", modelString)}
	for _, key := range []string{"observedAt", "occurredAt", "validFrom", "validUntil", "internalUse"} {
		fields = append(fields, scOpt(key, modelString))
	}
	fields = append(fields, scOpt("lineageStatus", scEnumeration("verified", "unverified", "quarantined")), scOpt("parentBinding", scEnumeration("bound", "legacy_unknown")), scOpt("supersedesId", modelString), scF("sources", scList(modelSource, 0, int(^uint(0)>>1))))
	return modelStrip(v, fields...)
}
func modelProof(v any) (any, error) {
	o, e := scShape(v, scF("sources", scList(scSource, 0, 128)), scF("parents", scList(scIdValue, 0, 128)), scOpt("parentReferences", scList(scRef, 0, 128)), scOpt("subjectWitness", scWitness), scOpt("sourceObservations", scList(scObservation, 0, 128)))
	if e != nil {
		return nil, e
	}
	// Reuse exactly the parentBinding validation while adding no ID to output.
	withID := o.Clone()
	_ = withID.Set("proofId", "model-schema-check")
	if _, e = scProof(withID); e != nil {
		return nil, e
	}
	return o, nil
}
func modelParent(v any) (any, error) {
	return scShape(v, scF("ref", scRef), scF("proofs", scList(modelProof, 1, 128)))
}
func modelDeclaration(v any) (any, error) {
	return scShape(v, scF("ref", scRef), scF("record", modelRecord))
}
func modelEpisode(v any) (any, error) {
	return scShape(v, scF("id", modelString), scF("version", scSafe(1)))
}
func modelItem(v any) (any, error) {
	o, e := scShape(v, scF("id", modelString), scF("version", scSafe(1)), scF("personId", modelNullableString), scF("kind", scEnumeration("summary_candidate", "duplicate_candidate", "episode_candidate")), scF("text", modelText), scF("selectedParentIds", scList(modelString, 2, 12)), scF("status", scEnumeration("candidate", "admitted")), scF("admission", scEnumeration("qualified_candidate", "structural_low_risk_summary", "administrator_review")), scF("hypothesis", scBoolean), scF("anchorQualifications", scList(scQualification, 0, 40)), scF("createdAt", modelString), scF("applicableUntil", modelNullableString), scOpt("episodeAnchor", modelEpisode), scF("parents", scList(modelParent, 2, 12)), scF("declarations", scList(modelDeclaration, 2, 12)), scF("rootCount", scSafe(0)), scF("applicability", scEnumeration("current", "review_needed")), scF("interpretation", scEnumeration("attributed_summary_not_verified_truth", "possible_duplicate_not_merged", "grouped_declarations_not_observed_event")))
	if e != nil {
		return nil, e
	}
	kind := scGet(o, "kind")
	if (kind == "episode_candidate") != (len(scArr(scGet(o, "anchorQualifications"))) > 0) {
		return nil, ErrContract
	}
	if kind == "summary_candidate" && scGet(o, "status") != "admitted" {
		return nil, ErrContract
	}
	expected := "grouped_declarations_not_observed_event"
	if kind == "summary_candidate" {
		expected = "attributed_summary_not_verified_truth"
	} else if kind == "duplicate_candidate" {
		expected = "possible_duplicate_not_merged"
	}
	if scGet(o, "interpretation") != expected {
		return nil, ErrContract
	}
	refs := map[string]any{}
	parents := scArr(scGet(o, "parents"))
	declarations := scArr(scGet(o, "declarations"))
	if len(parents) != len(declarations) {
		return nil, ErrContract
	}
	for _, parent := range parents {
		ref := scGet(parent, "ref")
		id := scStr(scGet(ref, "id"))
		if refs[id] != nil {
			return nil, ErrContract
		}
		refs[id] = ref
	}
	for _, d := range declarations {
		ref := scGet(d, "ref")
		id := scStr(scGet(ref, "id"))
		if id != scGet(scGet(d, "record"), "id") || !privacyEqual(ref, refs[id]) {
			return nil, ErrContract
		}
	}
	for _, id := range scArr(scGet(o, "selectedParentIds")) {
		if refs[scStr(id)] == nil {
			return nil, ErrContract
		}
	}
	return o, nil
}
func ValidateModelEvidence(v *jsjson.Object) (*jsjson.Object, error) {
	return scShape(v, scF("stateKind", scEnumeration("semantic_view")), scF("page", func(v any) (any, error) {
		return scShape(v, scF("coverage", scEnumeration("complete", "limited")), scF("sourceBodyCoverage", scEnumeration("unavailable")), scF("items", scList(modelItem, 0, 12)))
	}))
}
