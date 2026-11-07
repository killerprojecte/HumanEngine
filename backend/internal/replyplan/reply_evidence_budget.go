package replyplan

import "strings"

// PackReplyEvidenceInput consumes an already-authorized brief. Neither this
// packing operation nor its coverage metadata grants authority or entailment.
type PackReplyEvidenceInput struct {
	Brief, Envelope      *Object
	Serialize            func(ReadonlyJSON) (string, error)
	ContextLayout        string // empty or the source opt-in "messages"
	Budget               SelectionBudget
	MandatoryEvidenceIDs []string
	UpstreamOmissions    []*Object
}

type briefProjection struct {
	packed  *Object
	omitted []any
	payload *Object
}

// PackReplyEvidence keeps whole memory receipts and the transitive dependency
// groups induced by facts. The returned serialized text is the winning logical
// projection; usage measures the expanded wire layout when requested.
func (p *Policy) PackReplyEvidence(input PackReplyEvidenceInput) ReadonlyJSON {
	return ReadonlyJSON{p.packReplyEvidence(input)}
}

func (p *Policy) packReplyEvidence(input PackReplyEvidenceInput) (result *Object) {
	fail := func(reason string) *Object {
		return obj(member("status", "invalid"), member("omitted", []any{}), member("reason", reason))
	}
	defer func() {
		if recover() != nil {
			result = fail("invalid_reply_evidence")
		}
	}()
	upstream, upstreamIDs := []any{}, map[string]bool{}
	if len(input.UpstreamOmissions) > 12 {
		return fail("invalid_upstream_omission")
	}
	for _, item := range input.UpstreamOmissions {
		id, records := str(get(item, "evidenceId")), get(item, "omittedRecords")
		if id == "" || upstreamIDs[id] || get(item, "reason") != "planner_evidence_budget" || !safeInteger(records) || records.(float64) < 1 {
			return fail("invalid_upstream_omission")
		}
		upstreamIDs[id] = true
		upstream = append(upstream, item.Clone())
	}
	brief, envelope := object(clone(input.Brief)), object(clone(input.Envelope))
	memory, ids := []*Object{}, map[string]bool{}
	for _, value := range array(get(brief, "evidence")) {
		receipt := object(value)
		if !strings.HasPrefix(str(get(receipt, "toolName")), "memory.") {
			continue
		}
		memory = append(memory, receipt)
		ids[str(get(receipt, "id"))] = true
	}
	if len(memory) > 40 || len(ids) != len(memory) {
		return fail("candidate_limit_or_duplicate")
	}
	mandatory := stringSet(input.MandatoryEvidenceIDs)
	for id := range mandatory {
		if !ids[id] {
			return fail("invalid_mandatory_selection")
		}
	}
	missing, sizes := map[string]bool{}, map[string]int{}
	recordCount := 0
	for _, receipt := range memory {
		id, tool := str(get(receipt, "id")), str(get(receipt, "toolName"))
		isState := includes(stateEvidenceTools, tool)
		key := "memories"
		if isState {
			key = "states"
		}
		records := array(get(receipt, key))
		if isState {
			if includes(workspaceStateTools, tool) {
				if len(records) != 1 {
					return fail("state_record_limit")
				}
				valid, err := p.ValidWorkspaceState(tool, records[0])
				if err != nil {
					return fail("invalid_reply_evidence")
				}
				if !valid {
					return fail("state_record_limit")
				}
			} else if tool == "memory.commitments" {
				if len(records) > 12 {
					return fail("state_record_limit")
				}
			} else {
				for _, record := range records {
					if !isArray(get(record, "revisions")) || len(array(get(record, "revisions"))) > 20 {
						return fail("state_record_limit")
					}
				}
			}
			if includes([]string{"memory.history", "memory.commitmentHistory"}, tool) {
				total := 0
				for _, record := range records {
					total += len(array(get(record, "revisions")))
				}
				if total > 20 {
					return fail("state_record_limit")
				}
			}
		}
		size := len(records)
		if isState {
			size = 1
		}
		recordCount += size
		sizes[id] = size
		incomplete := truthy(get(receipt, "contentOmitted")) || len(records) == 0
		if !isState {
			for _, record := range records {
				if !isString(get(record, "content")) || truthy(get(record, "contentTruncated")) {
					incomplete = true
				}
			}
		}
		if incomplete {
			missing[id] = true
		}
	}
	if recordCount > 40 {
		return fail("candidate_record_limit")
	}
	for _, receipt := range memory {
		id := str(get(receipt, "id"))
		if mandatory[id] && missing[id] {
			omitted := []any{}
			for _, r := range memory {
				if missing[str(get(r, "id"))] {
					omitted = append(omitted, obj(member("evidenceId", get(r, "id")), member("reason", "missing_memory_selection")))
				}
			}
			return obj(member("status", "insufficient"), member("omitted", omitted), member("reason", "mandatory_incomplete"))
		}
	}
	roots := map[string]string{}
	for id := range ids {
		roots[id] = id
	}
	root := func(id string) string {
		for roots[id] != id {
			id = roots[id]
		}
		return id
	}
	facts := []any{}
	for _, fact := range array(get(get(brief, "plan"), "facts")) {
		refs, incomplete := []string{}, false
		for _, id := range memberStrings(get(fact, "evidenceIds")) {
			if missing[id] {
				incomplete = true
			}
			if ids[id] {
				refs = append(refs, id)
			}
		}
		if incomplete {
			continue
		}
		facts = append(facts, fact)
		if len(refs) > 1 {
			for _, id := range refs[1:] {
				roots[root(id)] = root(refs[0])
			}
		}
	}
	groupOrder, groups := []string{}, map[string][]string{}
	for _, receipt := range memory {
		id := str(get(receipt, "id"))
		if missing[id] {
			continue
		}
		key := root(id)
		if _, exists := groups[key]; !exists {
			groupOrder = append(groupOrder, key)
		}
		groups[key] = append(groups[key], id)
	}
	project := func(kept map[string]bool) briefProjection {
		omitted := []any{}
		for _, item := range memory {
			id := str(get(item, "id"))
			if kept[id] || upstreamIDs[id] {
				continue
			}
			reason := "budget_or_group_limit"
			if missing[id] {
				reason = "missing_memory_selection"
				if includes(stateEvidenceTools, str(get(item, "toolName"))) {
					reason = "missing_state_selection"
				}
			}
			omitted = append(omitted, obj(member("evidenceId", id), member("reason", reason)))
		}
		omitted = append(omitted, upstream...)
		plan := object(get(brief, "plan")).Clone()
		retainedFacts := []any{}
		for _, fact := range facts {
			retain := true
			for _, id := range memberStrings(get(fact, "evidenceIds")) {
				if ids[id] && !kept[id] {
					retain = false
					break
				}
			}
			if retain {
				retainedFacts = append(retainedFacts, fact)
			}
		}
		set(plan, "facts", retainedFacts)
		if len(omitted) > 0 {
			plan.Delete("replySuggestion")
			set(plan, "strategy", "Use only retained evidence; acknowledge missing information without reconstructing omitted memory.")
			set(plan, "expressionGoals", []any{})
		}
		packed := brief.Clone()
		set(packed, "plan", plan)
		retainedEvidence := []any{}
		for _, item := range array(get(brief, "evidence")) {
			id := str(get(item, "id"))
			if !ids[id] || kept[id] {
				retainedEvidence = append(retainedEvidence, item)
			}
		}
		set(packed, "evidence", retainedEvidence)
		status := "complete"
		if len(omitted) > 0 {
			status = "omitted"
		}
		payload := briefMerge(envelope, packed, obj(member("memoryEvidenceCoverage", obj(member("status", status), member("omitted", omitted)))))
		return briefProjection{packed, omitted, payload}
	}
	candidates := []any{}
	for index, id := range groupOrder {
		units, required := 0, false
		for _, ref := range groups[id] {
			units += sizes[ref]
			required = required || mandatory[ref]
		}
		candidates = append(candidates, obj(member("id", id), member("rank", float64(40-index)), member("mandatory", required), member("complete", true), member("units", float64(units)), member("value", stringsArray(groups[id]))))
	}
	logicalByWire := map[string]string{}
	serializer := func(values ReadonlyJSON) (string, error) {
		kept := map[string]bool{}
		for _, group := range array(values.value) {
			for _, id := range memberStrings(group) {
				kept[id] = true
			}
		}
		payload := project(kept).payload
		logical, err := input.Serialize(ReadonlyJSON{payload})
		if err != nil {
			return "", err
		}
		if input.ContextLayout == "" {
			return logical, nil
		}
		expanded, err := ExpandContextMessages([]*Object{obj(member("role", "user"), member("content", logical), member("contextLayout", input.ContextLayout))})
		if err != nil {
			return "", err
		}
		users := []*Object{}
		for _, m := range expanded {
			if get(m, "role") == "user" {
				users = append(users, m)
			}
		}
		wire, err := stringify(objectsValue(users))
		if err != nil {
			return "", err
		}
		logicalByWire[wire] = logical
		return wire, nil
	}
	selected := selectEvidenceGroups(SelectionInput{Candidates: candidates, Budget: input.Budget, Serialize: serializer})
	omissions := array(get(selected, "omitted"))
	if get(selected, "status") == "insufficient" && get(selected, "reason") == "no_complete_evidence" {
		selected = selectEvidenceGroups(SelectionInput{Candidates: []any{}, Budget: input.Budget, Serialize: serializer})
	}
	if get(selected, "status") != "selected" {
		return obj(member("status", get(selected, "status")), member("reason", briefProperty(selected, "reason")), member("omitted", project(map[string]bool{}).omitted))
	}
	kept := map[string]bool{}
	for _, group := range array(get(selected, "groups")) {
		for _, id := range memberStrings(get(group, "value")) {
			kept[id] = true
		}
	}
	projected := project(kept)
	diagnostics := []any{}
	for _, item := range projected.omitted {
		if includes([]string{"missing_memory_selection", "missing_state_selection", "planner_evidence_budget"}, str(get(item, "reason"))) {
			diagnostics = append(diagnostics, item)
			continue
		}
		reason := str(get(item, "reason"))
		for _, omission := range omissions {
			if includes(groups[str(get(omission, "id"))], str(get(item, "evidenceId"))) {
				r := str(get(omission, "reason"))
				if includes([]string{"record_limit", "group_limit", "budget"}, r) {
					reason = r
				}
				break
			}
		}
		out := object(item).Clone()
		set(out, "reason", reason)
		diagnostics = append(diagnostics, out)
	}
	serialized := get(selected, "serialized")
	if input.ContextLayout != "" {
		if logical, found := logicalByWire[str(serialized)]; found {
			serialized = logical
		} else {
			serialized = Undefined
		}
	}
	return obj(member("status", "selected"), member("brief", projected.packed), member("serialized", serialized), member("usage", get(selected, "usage")), member("omitted", diagnostics))
}
