package replyplan

import (
	"math"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// VerifiedTokenizer is a trusted server capability, not a client claim. Count
// must return a nonnegative safe integer, positive for nonempty input. Source
// behavior checks the final two measurements; it does not establish calibration.
type VerifiedTokenizer struct {
	Verified               bool
	Version, ModelIdentity string
	Count                  func(string) (float64, error)
}

// SelectionBudget is the reply selector's budget, distinct from the planner's
// accumulated EvidenceBudget. Limits 40/12 below are existing source policy.
type SelectionBudget struct {
	Mode      string
	Limit     float64
	Tokenizer *VerifiedTokenizer
}

type SelectionInput struct {
	// Candidates is []any of ordered Objects implementing the source EvidenceGroup
	// contract (string id, finite rank, boolean complete, optional units/mandatory).
	Candidates any
	// nil means no explicit selection; a non-nil empty slice selects none.
	SelectedIDs []string
	Budget      SelectionBudget
	// Serialize receives an immutable array of candidate values. It must preserve
	// complete meaning and project deterministically, just as the source contract.
	// Errors and panics produce invalid_input_or_measurement. Captured caller data
	// may change serially, but never concurrently; candidates were copied once.
	Serialize func(ReadonlyJSON) (string, error)
}

func immutableEvidenceJSON(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, string:
		return x, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, errCode("invalid_value")
		}
		return x, nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			var err error
			out[i], err = immutableEvidenceJSON(item)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case *Object:
		if x == nil {
			return nil, errCode("invalid_value")
		}
		out := obj()
		for _, m := range x.Members() {
			value, err := immutableEvidenceJSON(m.Value)
			if err != nil {
				return nil, err
			}
			set(out, m.Key, value)
		}
		return out, nil
	default:
		return nil, errCode("invalid_value")
	}
}

func selectionFailure(n int, status, reason string) *Object {
	return obj(member("status", status), member("reason", reason), member("groups", []any{}), member("omitted", []any{}), member("coverage", obj(member("candidates", float64(n)), member("selected", float64(0)), member("omitted", float64(n)))))
}

// SelectEvidenceGroups ports evidence-selector.ts. Its output is read-only like
// the source frozen groups. Copy returns an independent caller-owned tree.
func SelectEvidenceGroups(input SelectionInput) ReadonlyJSON {
	return ReadonlyJSON{selectEvidenceGroups(input)}
}

func selectEvidenceGroups(input SelectionInput) (result *Object) {
	countCandidates := len(array(input.Candidates))
	fail := func(status, reason string) *Object { return selectionFailure(countCandidates, status, reason) }
	defer func() {
		if recover() != nil {
			result = fail("invalid", "invalid_input_or_measurement")
		}
	}()
	if !isArray(input.Candidates) || countCandidates > 40 {
		return fail("invalid", "candidate_limit")
	}
	budget := input.Budget
	if !safeInteger(budget.Limit) || budget.Limit < 0 {
		return fail("invalid", "invalid_budget")
	}
	if budget.Mode != "bytes" && (budget.Mode != "tokens" || budget.Tokenizer == nil || !budget.Tokenizer.Verified || budget.Tokenizer.Version == "" || budget.Tokenizer.ModelIdentity == "") {
		return fail("invalid", "unverified_tokenizer")
	}
	ids := map[string]bool{}
	candidates := make([]*Object, countCandidates)
	for i, value := range array(input.Candidates) {
		group := object(value)
		id, idOK := get(group, "id").(string)
		rank, rankOK := get(group, "rank").(float64)
		units := get(group, "units")
		if !idOK || id == "" || ids[id] || !rankOK || math.IsNaN(rank) || math.IsInf(rank, 0) || defined(group, "units") && (!safeInteger(units) || units.(float64) < 1 || units.(float64) > 40) || !isBool(get(group, "complete")) {
			return fail("invalid", "invalid_input_or_measurement")
		}
		ids[id] = true
		copied, err := immutableEvidenceJSON(get(group, "value"))
		if !defined(group, "value") || err != nil {
			return fail("invalid", "invalid_input_or_measurement")
		}
		candidates[i] = group.Clone()
		set(candidates[i], "value", copied)
	}
	selected := stringSet(input.SelectedIDs)
	if input.SelectedIDs != nil {
		if len(selected) != len(input.SelectedIDs) {
			return fail("invalid", "invalid_selection")
		}
		for id := range selected {
			if !ids[id] {
				return fail("invalid", "invalid_selection")
			}
		}
		for _, g := range candidates {
			if truthy(get(g, "mandatory")) && !selected[str(get(g, "id"))] {
				return fail("invalid", "mandatory_not_selected")
			}
		}
	}
	type measured struct {
		text  string
		count float64
	}
	measure := func(groups []*Object) (measured, error) {
		values := make([]any, len(groups))
		for i, g := range groups {
			values[i] = get(g, "value")
		}
		serialized, err := input.Serialize(ReadonlyJSON{values})
		if err != nil {
			return measured{}, err
		}
		if _, err := jsjson.Parse(serialized); err != nil {
			return measured{}, err
		}
		var count float64
		if budget.Mode == "bytes" {
			utf8, err := jsjson.UTF8Text(serialized)
			if err != nil {
				return measured{}, err
			}
			count = float64(len(utf8))
		} else {
			count, err = budget.Tokenizer.Count(serialized)
			if err != nil {
				return measured{}, err
			}
		}
		if !safeInteger(count) || count < 0 || len(serialized) > 0 && count == 0 {
			return measured{}, errCode("invalid_count")
		}
		return measured{serialized, count}, nil
	}
	empty, err := measure(nil)
	if err != nil {
		return fail("invalid", "invalid_input_or_measurement")
	}
	if empty.count > budget.Limit {
		return fail("insufficient", "envelope_budget")
	}
	// JavaScript compares string IDs by UTF-16 code units, not UTF-8 bytes.
	unitIDs := map[string][]uint16{}
	for id := range ids {
		units, err := jsjson.UTF16(id)
		if err != nil {
			return fail("invalid", "invalid_input_or_measurement")
		}
		unitIDs[id] = units
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		am, bm := truthy(get(a, "mandatory")), truthy(get(b, "mandatory"))
		if am != bm {
			return am
		}
		ar, br := get(a, "rank").(float64), get(b, "rank").(float64)
		if ar != br {
			return ar > br
		}
		au, bu := unitIDs[str(get(a, "id"))], unitIDs[str(get(b, "id"))]
		for k := 0; k < len(au) && k < len(bu); k++ {
			if au[k] != bu[k] {
				return au[k] < bu[k]
			}
		}
		return len(au) < len(bu)
	})
	groups := []*Object{}
	omitted := []any{}
	totalUnits, anyUnits := float64(0), false
	for _, group := range candidates {
		id, mandatory := str(get(group, "id")), truthy(get(group, "mandatory"))
		omit := func(reason string) { omitted = append(omitted, obj(member("id", id), member("reason", reason))) }
		if get(group, "complete") != true {
			if mandatory {
				return fail("insufficient", "mandatory_incomplete")
			}
			omit("incomplete")
			continue
		}
		if input.SelectedIDs != nil && !selected[id] && !mandatory {
			omit("not_selected")
			continue
		}
		units, explicitUnits := float64(1), defined(group, "units")
		if explicitUnits {
			units = get(group, "units").(float64)
		}
		if totalUnits+units > 12 && (explicitUnits || anyUnits) {
			if mandatory {
				return fail("insufficient", "mandatory_record_limit")
			}
			omit("record_limit")
			continue
		}
		if len(groups) == 12 {
			if mandatory {
				return fail("insufficient", "mandatory_group_limit")
			}
			omit("group_limit")
			continue
		}
		measured, err := measure(append(append([]*Object{}, groups...), group))
		if err != nil {
			return fail("invalid", "invalid_input_or_measurement")
		}
		if measured.count > budget.Limit {
			if mandatory {
				return fail("insufficient", "mandatory_budget")
			}
			omit("budget")
			continue
		}
		groups = append(groups, group)
		totalUnits += units
		anyUnits = anyUnits || explicitUnits
	}
	if len(groups) == 0 && countCandidates > 0 {
		out := fail("insufficient", "no_complete_evidence")
		set(out, "omitted", omitted)
		return out
	}
	final, err := measure(groups)
	if err != nil {
		return fail("invalid", "invalid_input_or_measurement")
	}
	repeated, err := measure(groups)
	if err != nil {
		return fail("invalid", "invalid_input_or_measurement")
	}
	if final.text != repeated.text || final.count != repeated.count {
		return fail("invalid", "unstable_measurement")
	}
	if final.count > budget.Limit {
		return fail("insufficient", "final_budget")
	}
	usage := obj(member("unit", budget.Mode), member("count", final.count), member("limit", budget.Limit))
	if budget.Mode == "tokens" {
		set(usage, "tokenizerVersion", budget.Tokenizer.Version)
		set(usage, "modelIdentity", budget.Tokenizer.ModelIdentity)
	}
	return obj(member("status", "selected"), member("groups", objectsValue(groups)), member("serialized", final.text), member("usage", usage), member("omitted", omitted), member("coverage", obj(member("candidates", float64(countCandidates)), member("selected", float64(len(groups))), member("omitted", float64(len(omitted))))))
}
