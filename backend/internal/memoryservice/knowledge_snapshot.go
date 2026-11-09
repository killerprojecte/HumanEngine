package memoryservice

import "errors"

// knowledgeSnapshot preserves the complete query and support closure. A changed
// scene is an invalid page error, not an empty successful read (source contract).
func knowledgeSnapshot(scene string, depth float64, page *Object) (*Object, error) {
	invalid := errors.New("knowledge_exposure_page_invalid")
	edges, ok := value(page, "edges").([]any)
	if !ok {
		return nil, invalid
	}
	closure := object(value(page, "supportClosure"))
	support, ok := value(closure, "edges").([]any)
	if !ok {
		return nil, invalid
	}
	records, ok := value(closure, "records").([]any)
	if !ok {
		return nil, invalid
	}
	paths, ok := value(page, "paths").([]any)
	if !ok {
		return nil, invalid
	}
	if text(page, "interpretation") != "attributed_claims_not_transitive_truth" || (text(page, "coverage") != "complete" && text(page, "coverage") != "limited") || len(edges)+len(support) > 40 || len(records) > 128 || len(paths) > 64 {
		return nil, invalid
	}
	ids := map[string]bool{}
	for _, raw := range edges {
		ids[text(object(raw), "id")] = true
	}
	for _, raw := range append(append([]any{}, edges...), support...) {
		if text(object(raw), "sceneId") != scene {
			return nil, invalid
		}
	}
	for _, raw := range records {
		item := object(raw)
		ref := object(value(item, "ref"))
		record := object(value(item, "record"))
		scope := object(value(record, "scope"))
		if text(ref, "id") != text(record, "id") || text(scope, "kind") != "scene" || text(scope, "sceneId") != scene {
			return nil, invalid
		}
	}
	for _, raw := range paths {
		path, ok := raw.([]any)
		if !ok || float64(len(path)) > depth {
			return nil, invalid
		}
		for _, id := range path {
			s, ok := id.(string)
			if !ok || !ids[s] {
				return nil, invalid
			}
		}
	}
	return relationObject("stateKind", "knowledge_graph", "page", exposureClone(page)), nil
}

// Source order matches KnowledgeExposure: closure records, then each main and
// support edge's proof sources, spans, read dependencies, and dependencies.
func knowledgeSourceIDs(values []*Object) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	add := func(raw any) error {
		s, ok := raw.(string)
		if !ok || s == "" {
			return errors.New("state_exposure_source_invalid")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
		return nil
	}
	array := func(o *Object, k string) []any { v, _ := value(o, k).([]any); return v }
	sources := func(o *Object) error {
		for _, s := range array(o, "sources") {
			if err := add(value(object(s), "messageId")); err != nil {
				return err
			}
		}
		return nil
	}
	proofs := func(o *Object) error {
		for _, p := range array(o, "proofs") {
			if err := sources(object(p)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, v := range values {
		page := object(value(v, "page"))
		closure := object(value(page, "supportClosure"))
		for _, r := range array(closure, "records") {
			if err := sources(object(value(object(r), "record"))); err != nil {
				return nil, err
			}
		}
		for _, raw := range append(append([]any{}, array(page, "edges")...), array(closure, "edges")...) {
			edge := object(raw)
			if err := proofs(edge); err != nil {
				return nil, err
			}
			for _, s := range array(edge, "spans") {
				if err := add(value(object(value(object(s), "source")), "messageId")); err != nil {
					return nil, err
				}
			}
			for _, key := range []string{"readDependencies", "dependencies"} {
				for _, d := range array(edge, key) {
					if err := proofs(object(d)); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return out, nil
}
