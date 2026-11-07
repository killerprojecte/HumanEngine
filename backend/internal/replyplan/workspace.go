package replyplan

func validRef(v any) bool {
	return object(v) != nil && isString(get(v, "id")) && str(get(v, "id")) != "" && positiveCount(get(v, "version")) && count(get(v, "deletionEpoch"))
}
func validRecord(v any) bool {
	return object(v) != nil && isString(get(v, "id")) && isString(get(v, "content")) && isString(get(v, "kind")) && oneOf(get(v, "status"), "active", "disputed")
}
func validCommitmentPlan(v any) bool {
	if defined(v, "terms") {
		t := get(v, "terms")
		if object(t) == nil || get(t, "kind") != "plan" || !isString(get(t, "title")) || !every(get(t, "participantIds"), isString) || !every(get(t, "conditions"), isString) || !oneOf(get(t, "polarity"), "positive", "negative") || !oneOf(get(t, "modality"), "asserted", "possible") || !oneOf(get(t, "temporalScope"), "current", "past", "future", "unspecified") {
			return false
		}
	}
	return object(v) != nil && validRef(get(v, "ref")) && get(v, "qualification") == "declared_not_verified" && get(v, "sourceBodyCoverage") == "unavailable" && oneOf(get(v, "lifecycle"), "pending", "done", "cancelled", "needs_review") && oneOf(get(v, "confirmation"), "proposed", "confirmed", "incomplete")
}
func keysOnly(v any, names ...string) bool {
	o := object(v)
	if o == nil {
		return false
	}
	for _, m := range o.Members() {
		if !includes(names, m.Key) {
			return false
		}
	}
	return true
}

// ValidWorkspaceState validates a DTO only. It does not grant memory access or
// replace the memory subsystem's exposure/delivery authority validation.
func (p *Policy) ValidWorkspaceState(tool string, value any) (bool, error) {
	if tool == "memory.summaries" {
		return validSemanticState(value), nil
	}
	v := object(value)
	page := get(v, "page")
	if tool == "memory.commitmentTimeline" {
		anchor := get(v, "anchor")
		window := get(v, "window")
		if v == nil || get(v, "stateKind") != "commitment_timeline" || !validRef(anchor) || !positiveCount(get(anchor, "mutationVersion")) || get(v, "coverage") != "limited" || get(v, "interpretation") != "previously_recorded_declaration_state_not_current_truth" || get(v, "sourceBodyCoverage") != "unavailable" || get(v, "stateTime") != "server_recorded_not_occurred" || !every(get(v, "reasons"), isString) || object(window) == nil || get(window, "maxTransitions") != float64(64) || !deepEqual(get(window, "throughMutationVersion"), get(anchor, "mutationVersion")) || !isArray(get(v, "recordedStates")) || len(array(get(v, "recordedStates"))) > 20 {
			return false, nil
		}
		for _, r := range array(get(v, "recordedStates")) {
			if !isString(get(r, "recordedAt")) {
				return false, nil
			}
			valid, err := p.dates.Finite(str(get(r, "recordedAt")))
			if err != nil {
				return false, err
			}
			if !valid || !positiveCount(get(r, "mutationVersion")) || get(r, "mutationVersion").(float64) > get(anchor, "mutationVersion").(float64) || !oneOf(get(r, "status"), "available", "redacted") || !isArray(get(r, "states")) || len(array(get(r, "states"))) > 2 || get(r, "status") == "redacted" && len(array(get(r, "states"))) != 0 || !every(get(r, "states"), validCommitmentPlan) {
				return false, nil
			}
		}
		return true, nil
	}
	if tool == "memory.knowledge" {
		edges := get(page, "edges")
		paths := get(page, "paths")
		closure := get(page, "supportClosure")
		if v == nil || get(v, "stateKind") != "knowledge_graph" || object(page) == nil || !oneOf(get(page, "coverage"), "complete", "limited") || get(page, "interpretation") != "attributed_claims_not_transitive_truth" || !isArray(edges) || len(array(edges)) > 40 || !isArray(paths) || len(array(paths)) > 64 {
			return false, nil
		}
		if !every(edges, func(e any) bool {
			return truthy(e) && isString(get(e, "id")) && validRef(get(e, "backing")) && oneOf(get(e, "polarity"), "positive", "negative") && oneOf(get(e, "modality"), "asserted", "possible") && oneOf(get(e, "temporalScope"), "current", "past", "future", "unspecified") && isArray(get(e, "conditions")) && isArray(get(e, "proofs")) && isArray(get(e, "dependencies")) && isArray(get(e, "readDependencies")) && isArray(get(e, "spans"))
		}) || object(closure) == nil || !isArray(get(closure, "edges")) || !isArray(get(closure, "records")) || len(array(edges))+len(array(get(closure, "edges"))) > 40 || len(array(get(closure, "records"))) > 128 {
			return false, nil
		}
		return every(get(closure, "records"), func(v any) bool {
			return validRef(get(v, "ref")) && object(get(v, "record")) != nil && deepEqual(get(get(v, "ref"), "id"), get(get(v, "record"), "id"))
		}) && every(paths, func(path any) bool {
			return isArray(path) && len(array(path)) <= 2 && every(path, func(id any) bool { return some(edges, func(e any) bool { return deepEqual(get(e, "id"), id) }) })
		}), nil
	}
	if tool == "memory.identities" {
		items := get(page, "items")
		if v == nil || get(v, "stateKind") != "identity_view" || object(page) == nil || !keysOnly(v, "stateKind", "page") || !isString(get(page, "sceneId")) || !isString(get(page, "personId")) || !count(get(page, "revision")) || get(page, "coverage") != "complete" || get(page, "interpretation") != "administrator_verified_links_not_read_authority_or_personal_facts" || !isArray(items) || len(array(items)) == 0 || len(array(items)) > 32 {
			return false, nil
		}
		n := 0
		for _, entry := range array(items) {
			if object(entry) == nil || !isString(get(entry, "personId")) || !every(get(entry, "aliases"), func(alias any) bool {
				return object(alias) != nil && isString(get(alias, "label")) && oneOf(get(alias, "sourceKind"), "manual", "observation") && isNullOrString(alias, "validFrom") && isNullOrString(alias, "validUntil")
			}) {
				return false, nil
			}
			n += len(array(get(entry, "aliases")))
		}
		return n <= 64, nil
	}
	items := get(page, "items")
	if v == nil || !keysOnly(v, "stateKind", "page") || object(page) == nil || !isArray(items) || len(array(items)) > 12 {
		return false, nil
	}
	if tool == "memory.workingSet" {
		if get(v, "stateKind") != "working_set" || !isBool(get(page, "enabled")) || !oneOf(get(page, "coverage"), "complete", "limited", "disabled") || !count(get(page, "omittedCold")) {
			return false, nil
		}
		if get(page, "enabled") == false {
			return get(page, "coverage") == "disabled" && len(array(items)) == 0, nil
		}
		if get(page, "coverage") == "disabled" || get(page, "interpretation") != "accessibility_not_truth_or_absence" || !count(get(page, "omittedEntries")) {
			return false, nil
		}
		return every(items, func(x any) bool {
			a := get(x, "accessibility")
			if object(x) == nil || object(a) == nil || !oneOf(get(a, "state"), "hot", "cold", "unknown") || !isBool(get(a, "reviewDue")) || !isBool(get(a, "pinIntent")) {
				return false
			}
			if get(x, "kind") == "assertion" {
				snapshot := get(x, "snapshot")
				return validRef(get(snapshot, "ref")) && validRecord(get(snapshot, "record"))
			}
			return get(x, "kind") == "commitment" && validCommitmentPlan(get(x, "view"))
		}), nil
	}
	if tool == "memory.episodes" {
		coverage := get(page, "coverage")
		if get(v, "stateKind") != "experience_view" || get(page, "interpretation") != "independent_qualified_records_not_synthesized_episodes" || get(page, "sourceBodyCoverage") != "unavailable" || object(coverage) == nil || !oneOf(get(coverage, "status"), "complete", "limited") || !isArray(get(coverage, "reasons")) {
			return false, nil
		}
		for _, reason := range array(get(coverage, "reasons")) {
			s, err := jsString(reason)
			if err != nil {
				return false, err
			}
			if !includes([]string{"upstream_limited", "entry_limit"}, s) {
				return false, nil
			}
		}
		if get(coverage, "returned") != float64(len(array(items))) || !count(get(coverage, "omittedEntries")) {
			return false, nil
		}
		return every(items, func(x any) bool {
			if object(x) == nil {
				return false
			}
			if get(x, "kind") == "reported_experience" {
				record := get(x, "record")
				t := get(x, "time")
				return validRef(get(x, "assertionRef")) && validRecord(record) && get(record, "kind") == "experience" && object(t) != nil && isNullOrString(t, "occurredAt") && isString(get(t, "recordedAt"))
			}
			return get(x, "kind") == "shared_plan_declaration" && validRef(get(x, "commitmentRef")) && validCommitmentPlan(get(x, "view")) && get(x, "completionBasis") == "participant_self_report_not_observed_event"
		}), nil
	}
	return false, nil
}

func validSemanticState(value any) bool {
	parsed, ok := parseSchema(semanticSchema, value)
	if !ok {
		return false
	}
	for _, v := range array(get(get(parsed, "page"), "items")) {
		kind := str(get(v, "kind"))
		if (kind == "episode_candidate") != (len(array(get(v, "anchorQualifications"))) > 0) || kind == "summary_candidate" && get(v, "status") != "admitted" {
			return false
		}
		expected := "grouped_declarations_not_observed_event"
		if kind == "summary_candidate" {
			expected = "attributed_summary_not_verified_truth"
		} else if kind == "duplicate_candidate" {
			expected = "possible_duplicate_not_merged"
		}
		parents := array(get(v, "parents"))
		declarations := array(get(v, "declarations"))
		if get(v, "interpretation") != expected || len(parents) != len(declarations) {
			return false
		}
		ids := []string{}
		for _, parent := range parents {
			ids = append(ids, str(get(get(parent, "ref"), "id")))
			for _, proof := range array(get(parent, "proofs")) {
				if !validParentBinding(proof) {
					return false
				}
			}
		}
		if !unique(ids) {
			return false
		}
		for _, d := range declarations {
			ref := get(d, "ref")
			if !deepEqual(get(ref, "id"), get(get(d, "record"), "id")) || !some(get(v, "parents"), func(p any) bool { return deepEqual(get(p, "ref"), ref) }) {
				return false
			}
		}
		for _, id := range memberStrings(get(v, "selectedParentIds")) {
			if !includes(ids, id) {
				return false
			}
		}
	}
	return true
}
func validParentBinding(proof any) bool {
	parents := memberStrings(get(proof, "parents"))
	if !unique(parents) {
		return false
	}
	for _, id := range parents {
		if id == "" {
			return false
		}
	}
	if !defined(proof, "parentReferences") {
		return true
	}
	refs := array(get(proof, "parentReferences"))
	if len(refs) != len(parents) {
		return false
	}
	ids := []string{}
	for _, ref := range refs {
		id := str(get(ref, "id"))
		ids = append(ids, id)
		if !includes(parents, id) || !positiveCount(get(ref, "version")) || !count(get(ref, "deletionEpoch")) {
			return false
		}
	}
	return unique(ids)
}
