package replyplan

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestWithinSignalDoesNotWaitForIgnoringExtension(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	ready := make(chan struct{})
	var settle func(string, error)
	returned := make(chan error, 1)
	go func() {
		_, err := withinSignal(ctx, func(done func(string, error)) { settle = done }, func() { close(ready) })
		returned <- err
	}()
	<-ready
	cause := errors.New("cancelled_stage")
	cancel(cause)
	select {
	case err := <-returned:
		if err != cause {
			t.Fatalf("cause lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("await did not cancel promptly")
	}
	settle("late", nil)
	settle("duplicate", errors.New("late_error"))
}

func TestConcurrentLateSettlementsAreHarmless(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	var settle func(int, error)
	done := make(chan struct{})
	go func() {
		_, _ = withinSignal(ctx, func(callback func(int, error)) { settle = callback }, func() { close(ready) })
		close(done)
	}()
	<-ready
	cancel()
	<-done
	var callers sync.WaitGroup
	for i := 0; i < 64; i++ {
		callers.Add(1)
		go func(i int) { defer callers.Done(); settle(i, nil) }(i)
	}
	callers.Wait()
}

func TestUndefinedAndStrictEqualityDataBoundary(t *testing.T) {
	value := obj(member("absent", Undefined), member("null", nil), member("items", []any{Undefined, nil, math.Inf(1), math.Copysign(0, -1)}))
	got, err := stringify(value)
	if err != nil || got != "{\"null\":null,\"items\":[null,null,null,0]}" {
		t.Fatalf("%s %v", got, err)
	}
	if deepEqual(float64(0), math.Copysign(0, -1)) {
		t.Fatal("signed zero collapsed")
	}
	if !deepEqual(math.NaN(), math.NaN()) {
		t.Fatal("NaN strict deep equality differs")
	}
	if deepEqual(nil, Undefined) {
		t.Fatal("null and undefined conflated")
	}
	a := obj(member("a", float64(1)), member("b", float64(2)))
	b := obj(member("b", float64(2)), member("a", float64(1)))
	if !deepEqual(a, b) {
		t.Fatal("object key order incorrectly affects strict deep equality")
	}
}

func TestSchemaUnknownRetainsValueAndDefaults(t *testing.T) {
	nested := obj(member("x", float64(1)))
	plan := object(parse(t, `{"action":"reply","targetMessageIds":[],"contextMessageIds":[],"strategy":"","constraints":[],"facts":[],"assets":[],"emoji":[],"actions":[{"toolName":"learning.propose","arguments":{}}]}`))
	set(object(get(array(get(plan, "actions"))[0], "arguments")), "nested", nested)
	set(plan, "deliveryTool", Undefined)
	parsed, err := parsePlan(plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if get(parsed, "deliveryTool") != "messages.send" {
		t.Fatal("undefined default not applied")
	}
	if get(get(array(get(parsed, "actions"))[0], "arguments"), "nested") != nested {
		t.Fatal("Zod unknown identity was copied")
	}
}

func TestOriginalPolicyLimitsAndNoNewArgumentCap(t *testing.T) {
	if PlannerMaxRounds != 6 || PlannerMaxToolCalls != 12 || DialogueMaxRounds != 3 {
		t.Fatal("source product budgets changed")
	}
	plan := object(parse(t, `{"action":"reply","targetMessageIds":[],"contextMessageIds":[],"strategy":"","constraints":[],"facts":[],"assets":[],"emoji":[],"actions":[{"toolName":"learning.propose","arguments":{}}]}`))
	arguments := object(get(array(get(plan, "actions"))[0], "arguments"))
	set(arguments, "large", strings.Repeat("x", 300000))
	if _, err := parsePlan(plan, false); err != nil {
		t.Fatalf("introduced action argument cap: %v", err)
	}
	maximum := float64(9007199254740991)
	if _, err := NewEvidenceBudget(&maximum); err != nil {
		t.Fatalf("introduced evidence configuration cap: %v", err)
	}
}

func TestPromptExpansionRetainsOpaqueMessageIdentity(t *testing.T) {
	assistant := obj(member("role", "assistant"), member("content", "continuation"))
	system := obj(member("role", "system"), member("content", "system"))
	plain := obj(member("role", "user"), member("content", "plain"))
	layout := obj(member("role", "user"), member("content", `{"messages":[]}`), member("contextLayout", "messages"))
	result, err := ExpandContextMessages([]*Object{system, assistant, plain, layout})
	if err != nil {
		t.Fatal(err)
	}
	if result[1] != assistant || result[2] != plain {
		t.Fatal("non-envelope message identity changed")
	}
	if result[0] == system || get(system, "content") != "system" {
		t.Fatal("system must be copied, not mutated")
	}
}

func TestRepairHintsUseUTF16NotSchemaCodePoints(t *testing.T) {
	id := strings.Repeat("🙂", 150)
	n, err := jsjson.CodePointCount(id)
	if err != nil || n != 150 {
		t.Fatal(n, err)
	}
	hint := repairHint("planner_invalid_plan", []*Object{obj(member("id", "r"), member("toolName", "memory.search"), member("value", []any{obj(member("id", id)), obj(member("id", "short"))}))})
	options := array(get(hint, "memorySelectionOptions"))
	if got := memberStrings(get(options[0], "memoryIds")); len(got) != 1 || got[0] != "short" {
		t.Fatalf("hint uses wrong length measure: %v", got)
	}
}
