package replyplan

import (
	"math"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
)

func handoffPolicy(t *testing.T) *Policy {
	t.Helper()
	dates, err := jsdate.New("UTC")
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(fixtureComparator(loadOracle(t)), dates)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestEvidenceReadonlyOwnership(t *testing.T) {
	candidate := parse(t, `{"id":"a","rank":1,"complete":true,"value":{"text":"qualified","nested":[{"condition":"only if confirmed"}]}}`)
	var retained []ReadonlyJSON
	calls := 0
	result := SelectEvidenceGroups(SelectionInput{Candidates: []any{candidate}, Budget: SelectionBudget{Mode: "bytes", Limit: 10000}, Serialize: func(values ReadonlyJSON) (string, error) {
		calls++
		retained = append(retained, values)
		if calls == 1 {
			set(object(get(candidate, "value")), "text", "caller changed")
		}
		copy := array(values.Copy())
		if len(copy) > 0 {
			set(object(copy[0]), "text", "callback copy changed")
			set(object(array(get(copy[0], "nested"))[0]), "condition", "callback copy changed")
		}
		return values.Stringify()
	}})
	if result.Get("status").value != "selected" || calls != 4 {
		t.Fatalf("result=%s calls=%d", encoded(t, result.value), calls)
	}
	copy := result.Copy()
	value := get(array(get(copy, "groups"))[0], "value")
	set(object(value), "text", "result copy changed")
	set(object(array(get(value, "nested"))[0]), "condition", "result copy changed")
	for _, view := range retained[1:] {
		if text, _ := view.At(0).Get("text").String(); text != "qualified" {
			t.Fatal(text)
		}
		if condition, _ := view.At(0).Get("nested").At(0).Get("condition").String(); condition != "only if confirmed" {
			t.Fatal(condition)
		}
	}
	if text, _ := result.Get("groups").At(0).Get("value").Get("text").String(); text != "qualified" {
		t.Fatal(text)
	}
	keys := result.Get("groups").At(0).Get("value").Keys()
	keys[0] = "changed"
	if result.Get("groups").At(0).Get("value").Keys()[0] != "text" {
		t.Fatal("Keys leaked an alias")
	}
	if !result.Get("absent").IsUndefined() || !result.Get("groups").At(-1).IsUndefined() || !result.Get("groups").At(1).IsUndefined() {
		t.Fatal("missing access is not undefined")
	}
}

func TestPackedReadonlyOwnershipAndExactWire(t *testing.T) {
	p := handoffPolicy(t)
	brief := object(parse(t, `{"plan":{"action":"reply","strategy":"qualified","expressionGoals":[],"facts":[{"text":"Qualified","evidenceIds":["m"]}]},"evidence":[{"id":"m","toolName":"memory.search","memories":[{"id":"r","content":"not permanent"}]}],"actionReceipts":[],"imageContext":[]}`))
	envelope := object(parse(t, `{"messages":[{"id":"m1","text":"literal"}],"timezone":"UTC","now":"then"}`))
	var retained []ReadonlyJSON
	calls := 0
	result := p.PackReplyEvidence(PackReplyEvidenceInput{Brief: brief, Envelope: envelope, ContextLayout: "messages", Budget: SelectionBudget{Mode: "bytes", Limit: 10000}, Serialize: func(payload ReadonlyJSON) (string, error) {
		calls++
		retained = append(retained, payload)
		if calls == 1 {
			set(object(get(brief, "plan")), "strategy", "caller changed")
			set(envelope, "now", "caller changed")
		}
		copy := payload.Copy()
		set(object(get(copy, "plan")), "strategy", "callback copy changed")
		return payload.Stringify()
	}})
	if result.Get("status").value != "selected" {
		t.Fatal(encoded(t, result.value))
	}
	copy := result.Copy()
	set(object(get(get(copy, "brief"), "plan")), "strategy", "result copy changed")
	set(object(array(get(get(copy, "brief"), "evidence"))[0]), "memories", []any{})
	for _, payload := range retained {
		if now, _ := payload.Get("now").String(); now != "then" {
			t.Fatal(now)
		}
	}
	if strategy, _ := retained[len(retained)-1].Get("plan").Get("strategy").String(); strategy != "qualified" {
		t.Fatal(strategy)
	}
	if result.Get("brief").Get("evidence").At(0).Get("memories").Len() != 1 {
		t.Fatal("mutable result alias")
	}
	logical, _ := result.Get("serialized").String()
	expanded, err := ExpandContextMessages([]*Object{obj(member("role", "user"), member("content", logical), member("contextLayout", "messages"))})
	if err != nil {
		t.Fatal(err)
	}
	users := []*Object{}
	for _, message := range expanded {
		if get(message, "role") == "user" {
			users = append(users, message)
		}
	}
	wire := encoded(t, objectsValue(users))
	used, _ := result.Get("usage").Get("count").Number()
	if used != float64(len(wire)) || used <= float64(len(logical)) {
		t.Fatalf("wire=%d logical=%d usage=%v", len(wire), len(logical), used)
	}
}

func TestBriefRetainsSourceShallowReferences(t *testing.T) {
	p := handoffPolicy(t)
	plan := object(parse(t, `{"action":"reply","targetMessageIds":["m1"],"contextMessageIds":["m1"],"strategy":"initial","constraints":[],"facts":[],"assets":[],"emoji":[],"memorySelections":[{"evidenceId":"m","memoryIds":["r"]}]}`))
	record := object(parse(t, `{"id":"r","content":"complete","parentBinding":{"parents":["p"]}}`))
	brief, err := p.BuildReplyBrief(plan, []*Object{obj(member("id", "m"), member("toolName", "memory.search"), member("value", []any{record}))}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if &array(get(get(brief, "plan"), "targetMessageIds"))[0] != &array(get(plan, "targetMessageIds"))[0] {
		t.Fatal("brief unexpectedly deep-cloned source plan array")
	}
	set(object(get(record, "parentBinding")), "kind", "changed")
	memory := array(get(array(get(brief, "evidence"))[0], "memories"))[0]
	if get(get(memory, "parentBinding"), "kind") != "changed" {
		t.Fatal("brief unexpectedly deep-cloned picked nested data")
	}
	set(plan, "strategy", "replaced")
	if get(get(brief, "plan"), "strategy") != "initial" {
		t.Fatal("brief plan must be its own shallow object")
	}
}

func TestHandoffNoNewContentOrBudgetCaps(t *testing.T) {
	content := strings.Repeat("中文😀", 40000) + "但未确认，必须保留尾部条件"
	candidate := obj(member("id", "a"), member("rank", float64(1)), member("complete", true), member("value", obj(member("content", content))))
	result := SelectEvidenceGroups(SelectionInput{Candidates: []any{candidate}, Budget: SelectionBudget{Mode: "bytes", Limit: 9007199254740991}, Serialize: func(v ReadonlyJSON) (string, error) { return v.Stringify() }})
	if result.Get("status").value != "selected" {
		t.Fatal(encoded(t, result.value))
	}
	if got, _ := result.Get("groups").At(0).Get("value").Get("content").String(); got != content {
		t.Fatal("content truncated")
	}
	for _, invalid := range []any{Undefined, math.NaN(), math.Inf(1), map[string]any{"a": 1}} {
		set(candidate, "value", invalid)
		r := SelectEvidenceGroups(SelectionInput{Candidates: []any{candidate}, Budget: SelectionBudget{Mode: "bytes", Limit: 10000}, Serialize: func(v ReadonlyJSON) (string, error) { return v.Stringify() }})
		if r.Get("reason").value != "invalid_input_or_measurement" {
			t.Fatal(encoded(t, r.value))
		}
	}
}

func TestHandoffCallbackFailuresDoNotEchoPrivateErrors(t *testing.T) {
	for _, panicMode := range []bool{false, true} {
		r := SelectEvidenceGroups(SelectionInput{Candidates: []any{}, Budget: SelectionBudget{Mode: "bytes", Limit: 10000}, Serialize: func(ReadonlyJSON) (string, error) {
			if panicMode {
				panic("private content")
			}
			return "", errCode("private content")
		}})
		if r.Get("reason").value != "invalid_input_or_measurement" || strings.Contains(encoded(t, r.value), "private content") {
			t.Fatal(encoded(t, r.value))
		}
	}
	p := handoffPolicy(t)
	result := p.PackReplyEvidence(PackReplyEvidenceInput{Brief: object(parse(t, `{"plan":{"facts":[]},"evidence":[]}`)), Envelope: obj(), Budget: SelectionBudget{Mode: "bytes", Limit: 1000}, Serialize: func(ReadonlyJSON) (string, error) { panic("private content") }})
	if result.Get("reason").value != "invalid_input_or_measurement" || strings.Contains(encoded(t, result.value), "private content") {
		t.Fatal(encoded(t, result.value))
	}
}

func TestBriefSelectedVisionShortCircuitsOptionalReceiptProbe(t *testing.T) {
	p := handoffPolicy(t)
	plan := object(parse(t, `{"action":"reply","targetMessageIds":[],"contextMessageIds":[],"facts":[{"text":"qualified","evidenceIds":["m"]}],"assets":[],"emoji":[],"memorySelections":[]}`))
	receipt := object(parse(t, `{"id":"m","toolName":"memory.search","value":{"images":[{"messageId":{"toString":"shadow"}}]}}`))
	brief, err := p.BuildReplyBrief(plan, []*Object{receipt}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if get(array(get(brief, "evidence"))[0], "contentOmitted") != "memory_not_selected" {
		t.Fatal(encoded(t, brief))
	}
}
