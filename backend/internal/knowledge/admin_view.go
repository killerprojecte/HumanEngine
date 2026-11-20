package knowledge

// The administration view omits persisted internal qualification fields at
// each nested boundary: copied internal rows are never serialized directly.
func adminResult(value *Object, enabled bool, err error) (*Object, error) {
	if err != nil {
		return nil, err
	}
	closure := get(value, "supportClosure")
	out := adminFields(value, "paths", "coverage", "reason", "interpretation")
	_ = out.Set("enabled", enabled)
	_ = out.Set("edges", adminMap(get(value, "edges"), adminEdge))
	_ = out.Set("supportClosure", obj("edges", adminMap(get(closure, "edges"), adminEdge),
		"records", adminMap(get(closure, "records"), func(v any) *Object {
			record := get(v, "record")
			view := adminFields(record, "id", "content", "kind", "epistemicKind", "status", "origin", "learnedAt", "observedAt", "occurredAt", "validFrom", "validUntil", "internalUse")
			_ = view.Set("scope", adminFields(get(record, "scope"), "kind", "sceneId", "personId"))
			_ = view.Set("sources", adminMap(get(record, "sources"), adminSource))
			return obj("ref", get(v, "ref"), "record", view)
		})))
	_ = out.Set("sourceBodyCoverage", "unavailable")
	return out, nil
}

func adminEdge(v any) *Object {
	out := adminFields(v, "id", "version", "predicate", "property", "polarity", "modality", "temporalScope", "conditions", "validFrom", "validUntil", "createdAt", "backing", "backingStatus", "status", "admission", "applicability", "requiresPersonalization", "dependencyStatuses")
	_ = out.Set("subject", adminEndpoint(get(v, "subject")))
	_ = out.Set("object", adminEndpoint(get(v, "object")))
	_ = out.Set("proofs", adminMap(get(v, "proofs"), adminProof))
	_ = out.Set("dependencies", adminMap(get(v, "dependencies"), func(d any) *Object {
		return obj("entity", adminEntity(get(d, "entity")), "proofs", adminMap(get(d, "proofs"), adminProof))
	}))
	_ = out.Set("readDependencies", adminMap(get(v, "readDependencies"), func(d any) *Object {
		dep := adminFields(d, "backing", "edgeId", "edgeVersion")
		_ = dep.Set("proofs", adminMap(get(d, "proofs"), adminProof))
		return dep
	}))
	_ = out.Set("spans", adminMap(get(v, "spans"), func(s any) *Object {
		span := adminFields(s, "partIndex", "start", "end", "stance")
		_ = span.Set("source", adminSource(get(s, "source")))
		return span
	}))
	return out
}

func adminEndpoint(v any) *Object {
	switch get(v, "kind") {
	case "person":
		return adminFields(v, "kind", "personId")
	case "entity":
		return obj("kind", "entity", "entity", adminEntity(get(v, "entity")))
	default:
		return adminFields(v, "kind", "valueType", "value", "unit")
	}
}

func adminEntity(v any) *Object { return adminFields(v, "id", "sceneId", "kind", "title", "backing") }
func adminSource(v any) *Object { return adminFields(v, "sceneId", "messageId", "senderId") }
func adminProof(v any) *Object {
	out := adminFields(v, "parents", "parentReferences")
	_ = out.Set("sources", adminMap(get(v, "sources"), adminSource))
	return out
}
func adminFields(v any, keys ...string) *Object {
	out := new(Object)
	o := object(v)
	for _, key := range keys {
		if item, ok := o.Get(key); ok {
			_ = out.Set(key, item)
		}
	}
	return out
}
func adminMap(v any, project func(any) *Object) []any {
	items := arr(v)
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = project(item)
	}
	return out
}
