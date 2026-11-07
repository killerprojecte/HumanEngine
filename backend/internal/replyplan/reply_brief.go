package replyplan

import "strings"

func briefPick(value any, keys ...string) *Object {
	out := obj()
	for _, key := range keys {
		if defined(value, key) {
			set(out, key, get(value, key))
		}
	}
	return out
}
func briefMerge(values ...*Object) *Object {
	out := obj()
	for _, value := range values {
		if value != nil {
			for _, m := range value.Members() {
				set(out, m.Key, m.Value)
			}
		}
	}
	return out
}
func briefProperty(value any, key string) any {
	if !has(value, key) {
		return Undefined
	}
	return get(value, key)
}
func briefRef(value any) *Object { return briefPick(value, "id", "version", "deletionEpoch") }
func briefProvenance(value any) []any {
	out := []any{}
	for _, source := range array(value) {
		out = append(out, briefPick(source, "messageId", "senderId"))
	}
	return out
}

// KnowledgeEvidence retains all semantic fields and AND/OR groups, recursively
// removing only the source's three internal proof/hash fields.
func KnowledgeEvidence(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = KnowledgeEvidence(x)
		}
		return out
	case *Object:
		out := obj()
		for _, m := range v.Members() {
			if !includes([]string{"proofId", "backingFingerprint", "spanHash"}, m.Key) {
				set(out, m.Key, KnowledgeEvidence(m.Value))
			}
		}
		return out
	default:
		return value
	}
}

func briefMemory(value any) any {
	if object(value) == nil {
		return Undefined
	}
	out := briefPick(value, "id", "kind", "status", "lineageStatus", "parentBinding", "occurredAt", "learnedAt", "observedAt", "validUntil", "validFrom", "epistemicKind")
	scope := get(value, "scope")
	if get(scope, "kind") == "scene" && isString(get(scope, "personId")) {
		set(out, "personId", get(scope, "personId"))
	}
	if isString(get(value, "content")) {
		set(out, "content", get(value, "content"))
	}
	if truthy(get(value, "sources")) {
		set(out, "sources", briefProvenance(get(value, "sources")))
	}
	if isArray(get(value, "relationEvidence")) {
		set(out, "relationCoverage", briefProperty(value, "relationCoverage"))
		edges := []any{}
		for _, edge := range array(get(value, "relationEvidence")) {
			e := briefPick(edge, "id", "version", "predicate", "personId", "polarity", "modality", "temporalScope", "conditions", "occurredAt", "validFrom", "validUntil", "epistemicKind", "status", "admission", "autoReason", "applicability", "requiredMemoryIds")
			set(e, "target", briefPick(get(edge, "target"), "entityId", "kind", "title"))
			set(e, "sources", briefProvenance(get(edge, "sources")))
			edges = append(edges, e)
		}
		set(out, "relationEvidence", edges)
	}
	return out
}
func briefDeclaration(value any) *Object {
	out := briefPick(value, "kind", "personId")
	set(out, "backing", briefRef(get(value, "backing")))
	if truthy(get(value, "qualificationRef")) {
		set(out, "qualificationRef", briefRef(get(value, "qualificationRef")))
	}
	if isArray(get(value, "proofs")) {
		observations, sources := []any{}, []any{}
		for _, proof := range array(get(value, "proofs")) {
			for _, observation := range array(get(proof, "sourceObservations")) {
				o := briefPick(observation, "sceneId", "messageId", "senderId", "sentAt")
				if isString(get(observation, "receivedAt")) {
					set(o, "observedAt", get(observation, "receivedAt"))
				}
				observations = append(observations, o)
			}
			for _, source := range array(get(proof, "sources")) {
				s := briefPick(source, "sceneId", "messageId", "senderId")
				if truthy(get(source, "eventRef")) {
					set(s, "eventRef", briefPick(get(source, "eventRef"), "eventId", "revision", "deletionEpoch"))
				}
				sources = append(sources, s)
			}
		}
		set(out, "observations", observations)
		set(out, "sources", sources)
	}
	return out
}
func (p *Policy) briefState(value any) (any, error) {
	kind := str(get(value, "stateKind"))
	if kind == "semantic_view" {
		if !validSemanticState(value) {
			return Undefined, nil
		}
		parsed, ok := parseSchema(semanticSchema, value)
		if !ok {
			return Undefined, nil
		}
		return clone(parsed), nil
	}
	tool := map[string]string{"knowledge_graph": "memory.knowledge", "identity_view": "memory.identities", "working_set": "memory.workingSet", "experience_view": "memory.episodes"}[kind]
	if tool != "" {
		valid, err := p.ValidWorkspaceState(tool, value)
		if err != nil {
			return nil, err
		}
		if !valid {
			return Undefined, nil
		}
	}
	if kind == "knowledge_graph" {
		return KnowledgeEvidence(value), nil
	}
	page := get(value, "page")
	if kind == "identity_view" {
		items := []any{}
		for _, entry := range array(get(page, "items")) {
			aliases := []any{}
			for _, alias := range array(get(entry, "aliases")) {
				aliases = append(aliases, briefPick(alias, "label", "validFrom", "validUntil", "sourceKind", "sourceMessageId"))
			}
			items = append(items, obj(member("personId", briefProperty(entry, "personId")), member("aliases", aliases)))
		}
		outPage := briefPick(page, "sceneId", "revision", "personId", "coverage", "interpretation")
		set(outPage, "items", items)
		return obj(member("stateKind", "identity_view"), member("page", outPage)), nil
	}
	accessibility := func(value any) *Object {
		return briefPick(value, "state", "reviewDue", "pinned", "pinIntent", "pinStatus", "pinRevision", "policyCoverage", "metadataRevision", "contextSelectedCount", "lastSelectedAt", "protection")
	}
	viewOf := func(value any) (any, error) {
		state, err := p.briefState(obj(member("stateKind", "commitment"), member("view", value)))
		if err != nil {
			return nil, err
		}
		return briefProperty(state, "view"), nil
	}
	if kind == "working_set" || kind == "experience_view" {
		entries := []any{}
		for _, entry := range array(get(page, "items")) {
			e := obj(member("kind", briefProperty(entry, "kind")))
			switch get(entry, "kind") {
			case "assertion":
				snapshot := get(entry, "snapshot")
				set(e, "snapshot", obj(member("ref", briefRef(get(snapshot, "ref"))), member("record", briefMemory(get(snapshot, "record")))))
				set(e, "accessibility", accessibility(get(entry, "accessibility")))
			case "commitment":
				view, err := viewOf(get(entry, "view"))
				if err != nil {
					return nil, err
				}
				set(e, "view", view)
				set(e, "accessibility", accessibility(get(entry, "accessibility")))
			case "reported_experience":
				set(e, "assertionRef", briefRef(get(entry, "assertionRef")))
				set(e, "record", briefMemory(get(entry, "record")))
				set(e, "time", briefPick(get(entry, "time"), "occurredAt", "recordedAt"))
			default:
				set(e, "commitmentRef", briefMerge(briefRef(get(entry, "commitmentRef")), briefPick(get(entry, "commitmentRef"), "mutationVersion")))
				view, err := viewOf(get(entry, "view"))
				if err != nil {
					return nil, err
				}
				set(e, "view", view)
				set(e, "completionBasis", briefProperty(entry, "completionBasis"))
			}
			entries = append(entries, e)
		}
		outPage := briefPick(page, "enabled", "coverage", "omittedCold", "omittedEntries", "interpretation", "sourceBodyCoverage")
		set(outPage, "items", entries)
		return obj(member("stateKind", kind), member("page", outPage)), nil
	}
	if kind == "commitment" {
		view := get(value, "view")
		if object(view) == nil || !truthy(get(view, "ref")) {
			return Undefined, nil
		}
		out := briefPick(view, "mutationVersion", "sceneId", "recordedAt", "confirmation", "lifecycle", "qualification", "sourceBodyCoverage", "changed", "confirmedParticipantIds", "missingParticipantIds", "completedParticipantIds", "cancelledParticipantIds")
		set(out, "ref", briefRef(get(view, "ref")))
		if truthy(get(view, "terms")) {
			set(out, "terms", briefPick(get(view, "terms"), "kind", "title", "participantIds", "polarity", "modality", "temporalScope", "conditions", "timeQualification", "occurredAt", "validFrom", "validUntil"))
		}
		declarations := []any{}
		for _, item := range array(get(view, "evidence")) {
			declarations = append(declarations, briefDeclaration(item))
		}
		set(out, "declarations", declarations)
		groups := []any{}
		for _, group := range array(get(view, "requiredProposalGroups")) {
			entries := []any{}
			for _, item := range array(group) {
				entries = append(entries, briefDeclaration(item))
			}
			groups = append(groups, entries)
		}
		set(out, "inheritedProposalGroups", groups)
		set(out, "inheritedProposalSemantics", "All groups required; alternatives within each group are OR, not independent confirmations.")
		return briefMerge(obj(member("stateKind", "commitment"), member("view", out)), briefPick(value, "coverage")), nil
	}
	if kind == "commitment_timeline" && isArray(get(value, "recordedStates")) {
		out := briefMerge(obj(member("stateKind", kind)), briefPick(value, "coverage", "reasons", "interpretation", "sourceBodyCoverage", "stateTime", "asOfRecorded"))
		set(out, "anchor", briefMerge(briefRef(get(value, "anchor")), briefPick(get(value, "anchor"), "mutationVersion")))
		set(out, "window", briefPick(get(value, "window"), "maxTransitions", "firstRecordedAt", "firstMutationVersion", "prunedThroughMutationVersion", "throughMutationVersion"))
		rows := []any{}
		for _, row := range array(get(value, "recordedStates")) {
			if row == nil {
				return nil, errCode("Cannot read properties of null (reading 'states')")
			}
			if isUndefined(row) {
				return nil, errCode("Cannot read properties of undefined (reading 'states')")
			}
			r := briefPick(row, "recordedAt", "mutationVersion", "reason", "activeVersion", "latestVersion", "status")
			states := []any{}
			for _, v := range array(get(row, "states")) {
				view, err := viewOf(v)
				if err != nil {
					return nil, err
				}
				states = append(states, view)
			}
			set(r, "states", states)
			rows = append(rows, r)
		}
		set(out, "recordedStates", rows)
		return out, nil
	}
	if kind == "commitment_history" && isArray(get(value, "revisions")) {
		out := briefMerge(obj(member("stateKind", kind)), briefPick(value, "coverage", "interpretation", "sourceBodyCoverage"))
		set(out, "anchor", briefMerge(briefRef(get(value, "anchor")), briefPick(get(value, "anchor"), "mutationVersion")))
		set(out, "historyWindow", briefPick(get(value, "historyWindow"), "maxRevisions", "omittedRevisions", "throughVersion"))
		revisions := []any{}
		for _, v := range array(get(value, "revisions")) {
			view, err := viewOf(v)
			if err != nil {
				return nil, err
			}
			revisions = append(revisions, view)
		}
		set(out, "revisions", revisions)
		return out, nil
	}
	if kind == "assertion_history" && isArray(get(value, "revisions")) {
		out := briefMerge(obj(member("stateKind", kind)), briefPick(value, "kind", "interpretation", "sourceBodyCoverage", "asOfRecorded", "validAt", "coverage"))
		revisions := []any{}
		for _, r := range array(get(value, "revisions")) {
			entry := briefPick(r, "assertionId", "version", "recordedAt", "validFrom", "validTo", "validityCoverage")
			set(entry, "ref", briefRef(get(r, "ref")))
			set(entry, "record", briefMemory(get(r, "record")))
			revisions = append(revisions, entry)
		}
		set(out, "revisions", revisions)
		return out, nil
	}
	return Undefined, nil
}

var briefMetadata = []string{"trust", "reason", "status", "note", "truncated"}

func briefSourceMetadata(value any) *Object {
	out := briefPick(value, briefMetadata...)
	if defined(value, "coverage") {
		set(out, "resultCoverage", get(value, "coverage"))
	}
	return out
}
func briefWebSource(value any) *Object {
	return briefPick(value, "title", "url", "sourceUrl", "snippet", "publishedAt")
}
func briefVision(value any, allowed map[string]bool) (*Object, error) {
	out := briefPick(value, "trust", "framePolicy")
	for _, key := range []string{"images", "omitted"} {
		entries := []any{}
		for _, entry := range array(get(value, key)) {
			id, err := propertyString(entry, "messageId")
			if err != nil {
				return nil, err
			}
			if !allowed[id] {
				continue
			}
			keys := []string{"messageId", "reason"}
			if key == "images" {
				keys = []string{"messageId", "status", "description", "reason", "framePolicy"}
			}
			entries = append(entries, briefPick(entry, keys...))
		}
		set(out, key, entries)
	}
	return out, nil
}
func briefImages(contexts []any, allowed map[string]bool) ([]any, error) {
	out := []any{}
	for _, value := range contexts {
		if a, ok := value.([]any); ok {
			nested, err := briefImages(a, allowed)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
			continue
		}
		if object(value) == nil {
			continue
		}
		if isArray(get(value, "images")) || isArray(get(value, "omitted")) {
			projected, err := briefVision(value, allowed)
			if err != nil {
				return nil, err
			}
			if len(array(get(projected, "images"))) > 0 || len(array(get(projected, "omitted"))) > 0 {
				out = append(out, projected)
			}
			continue
		}
		if a, ok := get(value, "imageContext").([]any); ok {
			nested, err := briefImages(a, allowed)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
		}
	}
	return out, nil
}

// BuildReplyBrief is a language-module handoff, not canonical execution evidence.
// Use ContextProjection with original messages, clock, timezone for the prompt.
// The source shallowly retains plan fields and picked nested data: callers must
// not mutate these concurrently. PackReplyEvidence takes its own deep snapshot.
func (p *Policy) BuildReplyBrief(plan *Object, evidence, actionReceipts []*Object, imageContext []any, relatedMessageIDs []string) (*Object, error) {
	imageIDs := stringSet(append(append(append([]string{}, memberStrings(get(plan, "contextMessageIds"))...), memberStrings(get(plan, "targetMessageIds"))...), relatedMessageIDs...))
	memorySelections := map[string]map[string]bool{}
	selected := map[string]bool{}
	for _, selection := range array(get(plan, "memorySelections")) {
		id := str(get(selection, "evidenceId"))
		memorySelections[id] = stringSet(memberStrings(get(selection, "memoryIds")))
		selected[id] = true
	}
	for _, fact := range array(get(plan, "facts")) {
		for _, id := range memberStrings(get(fact, "evidenceIds")) {
			selected[id] = true
		}
	}
	for _, asset := range array(get(plan, "assets")) {
		selected[str(get(asset, "evidenceId"))] = true
	}
	stateIDs := stringSet(memberStrings(get(plan, "stateEvidenceIds")))
	for id := range stateIDs {
		selected[id] = true
	}
	sources, coverage := []any{}, []any{}
	for _, receipt := range evidence {
		id, tool := str(get(receipt, "id")), str(get(receipt, "toolName"))
		value := get(receipt, "value")
		base := obj(member("id", briefProperty(receipt, "id")))
		if truthy(get(receipt, "toolName")) {
			set(base, "toolName", get(receipt, "toolName"))
		}
		c := base.Clone()
		if truthy(get(receipt, "coverage")) {
			set(c, "coverage", briefMerge(object(get(receipt, "coverage")), obj(member("scope", "returned_tool_result"))))
		}
		set(c, "retrievalCoverage", RetrievalCoverage(tool))
		coverage = append(coverage, c)
		keep := selected[id]
		if !keep {
			for _, key := range []string{"images", "omitted"} {
				for _, image := range array(get(value, key)) {
					messageID, err := propertyString(image, "messageId")
					if err != nil {
						return nil, err
					}
					if imageIDs[messageID] {
						keep = true
						break
					}
				}
				if keep {
					break
				}
			}
		}
		if !keep {
			continue
		}
		if includes(stateEvidenceTools, tool) {
			if !stateIDs[id] {
				set(base, "contentOmitted", "state_not_selected")
				sources = append(sources, base)
				continue
			}
			states := []any{}
			for _, v := range array(value) {
				state, err := p.briefState(v)
				if err != nil {
					return nil, err
				}
				if truthy(state) {
					states = append(states, state)
				}
			}
			set(base, "states", states)
			set(base, "caution", "State declarations and historical assertions are qualified evidence, not verified current truth. Working-set hot/cold/pin/access counts are accessibility policy, not truth, successful recall, satisfaction or event verification. Experience entries remain independent reports; shared plans are participant declarations, not observed events. Preserve all conditions, incomplete confirmation, historical time and coverage.")
			sources = append(sources, base)
			continue
		}
		if strings.HasPrefix(tool, "memory.") {
			ids, ok := memorySelections[id]
			if !ok {
				out := briefMerge(briefPick(value, briefMetadata...), base)
				set(out, "contentOmitted", "memory_not_selected")
				sources = append(sources, out)
				continue
			}
			records := array(value)
			if !isArray(value) {
				if isArray(get(value, "items")) {
					records = array(get(value, "items"))
				} else if defined(value, "content") {
					records = []any{value}
				}
			}
			out := briefMerge(briefSourceMetadata(value), base)
			set(out, "caution", "Recalled information, not independently verified facts; preserve uncertainty and conflicts, including complete qualifications.")
			memories := []any{}
			for _, record := range records {
				memoryID, err := propertyString(record, "id")
				if err != nil {
					return nil, err
				}
				if ids[memoryID] {
					memory := briefMemory(record)
					if truthy(memory) {
						memories = append(memories, memory)
					}
				}
			}
			set(out, "memories", memories)
			if truthy(get(value, "sources")) {
				set(out, "sources", briefProvenance(get(value, "sources")))
			}
			sources = append(sources, out)
			continue
		}
		if isArray(get(value, "images")) || isArray(get(value, "omitted")) {
			vision, err := briefVision(value, imageIDs)
			if err != nil {
				return nil, err
			}
			sources = append(sources, briefMerge(base, vision))
			continue
		}
		if tool == "memes.search" {
			ids := map[string]bool{}
			for _, asset := range array(get(plan, "assets")) {
				if get(asset, "evidenceId") == id {
					ids[str(get(asset, "assetId"))] = true
				}
			}
			assets := []any{}
			for _, asset := range array(get(value, "items")) {
				assetID, err := propertyString(asset, "id")
				if err != nil {
					return nil, err
				}
				if ids[assetID] {
					a := briefMerge(obj(member("assetId", briefProperty(asset, "id"))), briefPick(asset, "available", "disabled"))
					set(a, "meaning", briefPick(get(asset, "tags"), "emotion", "intent", "memeMeaning"))
					assets = append(assets, a)
				}
			}
			out := briefMerge(briefSourceMetadata(value), base)
			set(out, "assets", assets)
			sources = append(sources, out)
			continue
		}
		if isString(get(value, "senderId")) && isArray(get(value, "parts")) {
			messageID, err := propertyString(value, "id")
			if err != nil {
				return nil, err
			}
			if includes(memberStrings(get(plan, "contextMessageIds")), messageID) {
				set(base, "messageId", briefProperty(value, "id"))
			} else {
				set(base, "message", briefPick(value, "id", "senderId", "senderDisplayName", "sentAt", "parts", "replyToId", "status"))
			}
			sources = append(sources, base)
			continue
		}
		if strings.HasPrefix(tool, "messages.") {
			out := briefMerge(briefSourceMetadata(value), base)
			ids := []any{}
			for _, item := range array(get(value, "items")) {
				if isString(get(item, "id")) {
					ids = append(ids, get(item, "id"))
				}
			}
			set(out, "messageIds", ids)
			sources = append(sources, out)
			continue
		}
		results := array(get(value, "results"))
		if !isArray(get(value, "results")) {
			results = array(get(value, "items"))
		}
		out := briefMerge(briefSourceMetadata(value), base, briefWebSource(value))
		if len(results) > 0 {
			rows := []any{}
			for _, r := range results {
				rows = append(rows, briefWebSource(r))
			}
			set(out, "sources", rows)
		}
		sources = append(sources, out)
	}
	outPlan := obj()
	for _, key := range []string{"action", "deliveryTool", "targetMessageIds", "contextMessageIds", "strategy", "constraints", "facts", "assets", "emoji"} {
		set(outPlan, key, briefProperty(plan, key))
	}
	goals := get(plan, "expressionGoals")
	if goals == nil || isUndefined(goals) {
		goals = []any{}
	}
	set(outPlan, "expressionGoals", goals)
	if defined(plan, "replySuggestion") {
		set(outPlan, "replySuggestion", get(plan, "replySuggestion"))
	}
	images, err := briefImages(imageContext, imageIDs)
	if err != nil {
		return nil, err
	}
	actions := []any{}
	for _, receipt := range actionReceipts {
		value := get(receipt, "value")
		status := "unconfirmed"
		if get(value, "ok") == false || defined(value, "error") {
			status = "failed"
		} else if isString(get(value, "status")) {
			status = str(get(value, "status"))
		} else if get(value, "ok") == true {
			status = "confirmed"
		}
		out := briefMerge(obj(member("id", briefProperty(receipt, "id")), member("toolName", briefProperty(receipt, "toolName"))), briefPick(value, "ok", "actionTaken", "scope", "assetId", "sourceMessageId", "positive", "disabled", "reason"))
		set(out, "status", status)
		if truthy(get(get(value, "memory"), "id")) {
			set(out, "memoryId", get(get(value, "memory"), "id"))
		}
		if truthy(get(get(value, "habit"), "id")) {
			set(out, "habitId", get(get(value, "habit"), "id"))
		}
		actions = append(actions, out)
	}
	return obj(member("coverageByEvidence", coverage), member("plan", outPlan), member("evidence", sources), member("imageContext", images), member("actionReceipts", actions)), nil
}
