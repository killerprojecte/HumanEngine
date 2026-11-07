// Package replyplan implements the source reply-plan policy. It does not own
// model transports, memory access, delivery authority, or the bot's runtime.
package replyplan

import (
	"fmt"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
)

const (
	PlannerMaxRounds    = 6
	PlannerMaxToolCalls = 12
	DialogueMaxRounds   = 3
)

type LocaleCompare func(a, b string) (int, error)
type Policy struct {
	compare LocaleCompare
	dates   *jsdate.Parser
}

// New requires the deployment's exact locale comparator and Date.parse policy.
// Pass topiccollation.New(sourceResolvedLocale).Compare and a jsdate parser for
// the source-resolved timezone. Neither silently follows the Go host locale.
func New(compare LocaleCompare, dates *jsdate.Parser) (*Policy, error) {
	if compare == nil {
		return nil, fmt.Errorf("replyplan: source locale comparator required")
	}
	if dates == nil {
		return nil, fmt.Errorf("replyplan: source date parser required")
	}
	return &Policy{compare: compare, dates: dates}, nil
}

type ValidationInput struct {
	TargetMessageIDs        []string
	ContextMessageIDs       []string
	Evidence                []*Object
	ActionTools             []*Object
	DeliveryTools           []*Object
	ReplySuggestionsEnabled bool
}

var workspaceStateTools = []string{"memory.workingSet", "memory.episodes", "memory.identities", "memory.knowledge", "memory.summaries", "memory.commitmentTimeline"}
var stateEvidenceTools = append([]string{"memory.commitments", "memory.history", "memory.commitmentHistory"}, workspaceStateTools...)
var readNames = append([]string{"messages.get", "messages.around", "messages.byUser", "memory.search", "memory.person", "memory.relations", "memory.commitments", "memory.commitmentHistory", "memory.history", "memory.source", "memes.search", "rules.list"}, workspaceStateTools...)
var effectNames = []string{"memes.feedback", "habits.feedback", "habits.propose", "learning.propose"}

func PlannerReadTools(tools []*Object) []*Object {
	out := make([]*Object, 0)
	for _, t := range tools {
		name := str(get(t, "name"))
		if name != "messages.send" && !includes(effectNames, name) && get(t, "effect") != "send" && (includes(readNames, name) || get(t, "effect") == "read") {
			out = append(out, t)
		}
	}
	return out
}
func PlannerActionTools(tools []*Object) []*Object {
	out := make([]*Object, 0)
	for _, t := range tools {
		name := str(get(t, "name"))
		if name != "messages.send" && get(t, "effect") != "send" && (includes(effectNames, name) || get(t, "effect") == "learningCandidate") {
			out = append(out, t)
		}
	}
	return out
}
func toolExists(tools []*Object, name string) bool {
	for _, t := range tools {
		if get(t, "name") == name {
			return true
		}
	}
	return false
}

func (p *Policy) ValidateReplyPlan(value any, input ValidationInput) (*Object, error) {
	plan, err := parsePlan(value, input.ReplySuggestionsEnabled)
	if err != nil {
		return nil, err
	}
	delivery := str(get(plan, "deliveryTool"))
	if delivery != "messages.send" {
		valid := false
		for _, t := range input.DeliveryTools {
			if get(t, "name") == delivery && get(t, "effect") == "send" {
				valid = true
			}
		}
		if !valid {
			return nil, errCode("planner_invalid_delivery_tool")
		}
	}
	contexts := stringSet(input.ContextMessageIDs)
	targetIDs := input.TargetMessageIDs
	if len(targetIDs) == 0 {
		targetIDs = input.ContextMessageIDs
	}
	targets := stringSet(targetIDs)
	for _, receipt := range input.Evidence {
		if !oneOf(get(receipt, "toolName"), "messages.get", "messages.around", "messages.byUser") {
			continue
		}
		for _, item := range array(get(get(receipt, "value"), "items")) {
			// Source accesses item.id directly, before considering its status.
			// Nullish malformed records must not silently expand/validate context.
			if item == nil {
				return nil, errCode("Cannot read properties of null (reading 'id')")
			}
			if isUndefined(item) {
				return nil, errCode("Cannot read properties of undefined (reading 'id')")
			}
			if isString(get(item, "id")) && get(item, "status") == "available" {
				id := str(get(item, "id"))
				contexts[id] = true
				targets[id] = true
			}
		}
	}
	evidence := map[string]*Object{}
	for _, e := range input.Evidence {
		id := str(get(e, "id"))
		if _, exists := evidence[id]; exists {
			return nil, errCode("planner_duplicate_evidence")
		}
		evidence[id] = e
	}
	planTargets := memberStrings(get(plan, "targetMessageIds"))
	planContexts := memberStrings(get(plan, "contextMessageIds"))
	for _, id := range planTargets {
		if !targets[id] || !includes(planContexts, id) {
			return nil, errCode("planner_invalid_target")
		}
	}
	for _, id := range planContexts {
		if !contexts[id] {
			return nil, errCode("planner_invalid_target")
		}
	}
	selections := array(get(plan, "memorySelections"))
	total := 0
	selectionIDs := []string{}
	for _, s := range selections {
		total += len(array(get(s, "memoryIds")))
		selectionIDs = append(selectionIDs, str(get(s, "evidenceId")))
		if !unique(memberStrings(get(s, "memoryIds"))) {
			return nil, errCode("planner_unknown_memory_evidence")
		}
	}
	if total > 12 || !unique(selectionIDs) {
		return nil, errCode("planner_unknown_memory_evidence")
	}
	for _, selection := range selections {
		receipt := evidence[str(get(selection, "evidenceId"))]
		records := get(receipt, "value")
		if !oneOf(get(receipt, "toolName"), "memory.search", "memory.person", "memory.relations") || !isArray(records) {
			return nil, errCode("planner_unknown_memory_evidence")
		}
		ids := memberStrings(get(selection, "memoryIds"))
		for _, id := range ids {
			if !some(records, func(item any) bool { return object(item) != nil && get(item, "id") == id }) {
				return nil, errCode("planner_unknown_memory_evidence")
			}
		}
		if get(receipt, "toolName") == "memory.relations" {
			if err := ValidateRelationSelection(array(records), ids); err != nil {
				return nil, err
			}
		}
	}
	stateIDs := memberStrings(get(plan, "stateEvidenceIds"))
	if !unique(stateIDs) {
		return nil, errCode("planner_unknown_state_evidence")
	}
	for _, id := range stateIDs {
		receipt := evidence[id]
		tool := str(get(receipt, "toolName"))
		value := get(receipt, "value")
		records := array(value)
		maximum := 20
		if tool == "memory.commitments" {
			maximum = 12
		} else if tool == "memory.commitmentHistory" {
			maximum = 1
		}
		if receipt == nil || !includes(stateEvidenceTools, tool) || !isArray(value) || len(records) == 0 || len(records) > maximum {
			return nil, errCode("planner_unknown_state_evidence")
		}
		if includes(workspaceStateTools, tool) {
			if len(records) != 1 {
				return nil, errCode("planner_unknown_state_evidence")
			}
			valid, err := p.ValidWorkspaceState(tool, records[0])
			if err != nil {
				return nil, err
			}
			if !valid {
				return nil, errCode("planner_unknown_state_evidence")
			}
			continue
		}
		total := 0
		for _, item := range records {
			if object(item) == nil {
				return nil, errCode("planner_unknown_state_evidence")
			}
			if tool == "memory.commitments" {
				if get(item, "stateKind") != "commitment" || (!isArray(get(item, "view")) && object(get(item, "view")) == nil) {
					return nil, errCode("planner_unknown_state_evidence")
				}
			} else {
				kind := "assertion_history"
				if tool == "memory.commitmentHistory" {
					kind = "commitment_history"
				}
				revisions := get(item, "revisions")
				if get(item, "stateKind") != kind || !isArray(revisions) || len(array(revisions)) == 0 || len(array(revisions)) > 20 {
					return nil, errCode("planner_unknown_state_evidence")
				}
				total += len(array(revisions))
				if tool == "memory.commitmentHistory" {
					if object(get(item, "anchor")) == nil && !isArray(get(item, "anchor")) {
						return nil, errCode("planner_unknown_state_evidence")
					}
					for _, view := range array(revisions) {
						if object(view) == nil || !has(view, "ref") {
							return nil, errCode("planner_unknown_state_evidence")
						}
					}
				}
			}
		}
		if includes([]string{"memory.history", "memory.commitmentHistory"}, tool) && total > 20 {
			return nil, errCode("planner_unknown_state_evidence")
		}
	}
	for _, fact := range array(get(plan, "facts")) {
		for _, id := range memberStrings(get(fact, "evidenceIds")) {
			if evidence[id] == nil {
				return nil, errCode("planner_unknown_evidence")
			}
		}
	}
	for _, asset := range array(get(plan, "assets")) {
		source := evidence[str(get(asset, "evidenceId"))]
		items := get(get(source, "value"), "items")
		if get(source, "toolName") != "memes.search" || !isArray(items) || !some(items, func(item any) bool { return object(item) != nil && deepEqual(get(item, "id"), get(asset, "assetId")) }) {
			return nil, errCode("planner_unknown_asset")
		}
	}
	keys := map[string]bool{}
	actions := array(get(plan, "actions"))
	for _, action := range actions {
		canonical, err := p.canonicalAction(action)
		if err != nil {
			return nil, err
		}
		key, err := stringify(canonical)
		if err != nil {
			return nil, err
		}
		if keys[key] {
			return nil, errCode("planner_duplicate_action")
		}
		keys[key] = true
	}
	for _, action := range actions {
		if !toolExists(input.ActionTools, str(get(action, "toolName"))) {
			return nil, errCode("planner_forbidden_action")
		}
	}
	if get(plan, "action") == "noReply" {
		if len(array(get(plan, "assets"))) > 0 {
			return nil, errCode("planner_invalid_no_reply")
		}
		for _, action := range actions {
			if !oneOf(get(action, "toolName"), "learning.propose", "experience.propose") {
				return nil, errCode("planner_invalid_no_reply")
			}
		}
	}
	return plan, nil
}
func (p *Policy) canonicalAction(v any) (any, error) {
	switch x := v.(type) {
	case *Object:
		members := x.Members()
		var failure error
		sort.SliceStable(members, func(i, j int) bool {
			if failure != nil {
				return false
			}
			n, err := p.compare(members[i].Key, members[j].Key)
			if err != nil {
				failure = err
				return false
			}
			return n < 0
		})
		if failure != nil {
			return nil, failure
		}
		out := obj()
		for _, m := range members {
			value, err := p.canonicalAction(m.Value)
			if err != nil {
				return nil, err
			}
			set(out, m.Key, value)
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			value, err := p.canonicalAction(v)
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	default:
		return v, nil
	}
}

// ValidateRelationSelection preserves every returned qualification and
// counterclaim for selected entity groups. It does not certify their truth.
func ValidateRelationSelection(records []any, ids []string) error {
	selected := stringSet(ids)
	targets := map[any]bool{}
	target := func(edge any) any {
		if !has(get(edge, "target"), "entityId") {
			return undefinedTarget{}
		}
		return get(get(edge, "target"), "entityId")
	}
	linked := func(v any) []any {
		out := []any{}
		for _, edge := range array(get(v, "relationEvidence")) {
			if object(edge) != nil {
				out = append(out, edge)
			}
		}
		return out
	}
	for _, item := range records {
		id, err := propertyString(item, "id")
		if err != nil {
			return err
		}
		if selected[id] {
			for _, edge := range linked(item) {
				v := target(edge)
				switch v.(type) {
				case nil, string, float64, bool, undefinedTarget:
					targets[v] = true
				}
			}
		}
	}
	for _, item := range records {
		record := object(item)
		if record == nil {
			return errCode("planner_incomplete_relation_evidence")
		}
		edges := linked(item)
		id, err := propertyString(record, "id")
		if err != nil {
			return err
		}
		relevant := selected[id]
		for _, edge := range edges {
			v := target(edge)
			switch v.(type) {
			case nil, string, float64, bool, undefinedTarget:
				relevant = relevant || targets[v]
			}
		}
		if !relevant {
			continue
		}
		if !selected[id] || len(edges) == 0 {
			return errCode("planner_incomplete_relation_evidence")
		}
		for _, edge := range edges {
			if !isString(get(get(edge, "target"), "entityId")) || !every(get(edge, "requiredMemoryIds"), func(id any) bool { return isString(id) && selected[str(id)] }) {
				return errCode("planner_incomplete_relation_evidence")
			}
		}
	}
	return nil
}

type undefinedTarget struct{}
