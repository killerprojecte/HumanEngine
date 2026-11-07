package replyplan

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type fixtureCase struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Input    string `json:"input"`
	Expected string `json:"expected"`
}
type sourceOracle struct {
	Node     string `json:"node"`
	Locale   string `json:"locale"`
	Timezone string `json:"timezone"`
	Sources  []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"sources"`
	Comparisons [][]any       `json:"comparisons"`
	Cases       []fixtureCase `json:"cases"`
	Builds      []fixtureCase `json:"builds"`
	Signals     []fixtureCase `json:"signals"`
}

func loadOracle(t *testing.T) sourceOracle {
	t.Helper()
	data, err := os.ReadFile("testdata/node.json")
	if err != nil {
		t.Fatal(err)
	}
	var f sourceOracle
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Node != "v24.22.1" {
		t.Fatal(f.Node)
	}
	return f
}
func parse(t *testing.T, s string) any {
	t.Helper()
	v, err := jsjson.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func encoded(t *testing.T, v any) string {
	t.Helper()
	s, err := stringify(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func objectSlice(v any) []*Object {
	out := []*Object{}
	for _, x := range array(v) {
		out = append(out, object(x))
	}
	return out
}
func validationInput(v any) ValidationInput {
	return ValidationInput{TargetMessageIDs: memberStrings(get(v, "targetMessageIds")), ContextMessageIDs: memberStrings(get(v, "contextMessageIds")), Evidence: objectSlice(get(v, "evidence")), ActionTools: objectSlice(get(v, "actionTools")), DeliveryTools: objectSlice(get(v, "deliveryTools")), ReplySuggestionsEnabled: get(v, "replySuggestionsEnabled") == true}
}
func captured(value any, err error) *Object {
	if err != nil {
		return obj(member("error", err.Error()))
	}
	return obj(member("value", value))
}
func fixtureComparator(f sourceOracle) LocaleCompare {
	values := map[[2]string]int{}
	for _, row := range f.Comparisons {
		values[[2]string{row[0].(string), row[1].(string)}] = int(row[2].(float64))
	}
	return func(a, b string) (int, error) {
		value, ok := values[[2]string{a, b}]
		if !ok {
			return 0, fmt.Errorf("test fixture comparator has no pair %q %q", a, b)
		}
		return value, nil
	}
}
func TestActualSourceOracle(t *testing.T) { f := loadOracle(t); runOracle(t, f, fixtureComparator(f)) }
func TestSourceHashes(t *testing.T) {
	f := loadOracle(t)
	for _, source := range f.Sources {
		data, err := os.ReadFile(filepath.Join("../../..", source.Path))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != source.SHA256 {
			t.Fatalf("source drift %s", source.Path)
		}
	}
}
func runOracle(t *testing.T, f sourceOracle, compare LocaleCompare) {
	t.Helper()
	dates, err := jsdate.New(f.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(compare, dates)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range f.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			in := parse(t, tc.Input)
			var value any
			var err error
			switch tc.Kind {
			case "string":
				v := get(in, "value")
				for _, index := range array(get(in, "undefinedElements")) {
					array(v)[int(index.(float64))] = Undefined
				}
				value, err = jsString(v)
			case "validateUndefined":
				for _, field := range memberStrings(get(in, "fields")) {
					set(object(get(in, "value")), field, Undefined)
				}
				value, err = p.ValidateReplyPlan(get(in, "value"), validationInput(get(in, "input")))
			case "validate":
				value, err = p.ValidateReplyPlan(get(in, "value"), validationInput(get(in, "input")))
			case "workspace":
				value, err = p.ValidWorkspaceState(str(get(in, "tool")), get(in, "value"))
			case "relation":
				err = ValidateRelationSelection(array(get(in, "records")), memberStrings(get(in, "ids")))
				value = true
			case "capabilities":
				tools := objectSlice(get(in, "tools"))
				value = obj(member("read", objectsValue(PlannerReadTools(tools))), member("action", objectsValue(PlannerActionTools(tools))))
			case "context":
				var messages []*Object
				messages, err = ExpandContextMessages(objectSlice(get(in, "messages")))
				value = objectsValue(messages)
			case "budget":
				max := get(in, "max").(float64)
				var budget *EvidenceBudget
				budget, err = NewEvidenceBudget(&max)
				if err == nil {
					var delivered DeliveredReceipt
					delivered, err = budget.Receipt(objectSlice(get(in, "messages")), object(get(in, "receipt")), str(get(in, "toolCallId")), nil)
					if err == nil {
						value = obj(member("receipt", delivered.Receipt), member("message", delivered.Message))
					}
				}
			case "projection":
				projection := NewContextProjection(str(get(in, "sceneId")))
				steps := []any{}
				for _, step := range array(get(in, "steps")) {
					var out any
					var failure error
					switch get(step, "op") {
					case "project":
						out, failure = projection.Project(get(step, "value"))
					case "restore":
						out, failure = projection.RestoreArguments(get(step, "value"))
					case "stringify":
						out, failure = projection.StringifyTrailing(get(step, "value"), memberStrings(get(step, "trailingFields")))
					}
					steps = append(steps, captured(out, failure))
				}
				value = steps
			default:
				t.Fatal(tc.Kind)
			}
			got := encoded(t, captured(value, err))
			if got != tc.Expected {
				t.Fatalf("got %s\nwant %s", got, tc.Expected)
			}
		})
	}
	for _, tc := range f.Builds {
		t.Run("build/"+tc.Name, func(t *testing.T) {
			got := encoded(t, runBuild(t, p, parse(t, tc.Input)))
			if got != tc.Expected {
				t.Fatalf("got %s\nwant %s", got, tc.Expected)
			}
		})
	}
}
func defaultScope() *Object {
	return obj(member("targetMessageIds", []any{"m1"}), member("contextMessageIds", []any{"m1", "m2"}), member("evidence", []any{obj(member("id", "source"), member("value", "Synthetic fact"))}), member("actionTools", []any{fixtureTool("learning.propose", ""), fixtureTool("experience.propose", ""), fixtureTool("memes.feedback", "")}))
}
func fixtureTool(name, effect string) *Object {
	o := obj(member("name", name), member("description", "Fixture"), member("inputSchema", obj()))
	if effect != "" {
		set(o, "effect", effect)
	}
	return o
}
func callsFrom(v any) ([]ToolCall, *Object) {
	a := array(v)
	calls := make([]ToolCall, len(a))
	for i, c := range a {
		calls[i] = ToolCall{str(get(c, "id")), str(get(c, "name")), get(c, "arguments")}
	}
	return calls, obj(member("role", "assistant"), member("content", ""), member("toolCalls", v))
}
func runBuild(t *testing.T, p *Policy, recipe any) *Object {
	t.Helper()
	scope := defaultScope()
	if patch := object(get(recipe, "scope")); patch != nil {
		for _, m := range patch.Members() {
			set(scope, m.Key, m.Value)
		}
	}
	events := []any{}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	eligible := get(recipe, "ineligibleBefore") != true
	round, executions := 0, 0
	input := BuildInput{ValidationInput: validationInput(scope), Messages: []*Object{obj(member("role", "user"), member("content", "Synthetic context"))}, ReadTools: []*Object{fixtureTool("memory.search", "")}, Eligible: func() bool { return eligible }}
	if has(recipe, "messages") {
		input.Messages = objectSlice(get(recipe, "messages"))
	}
	if has(recipe, "readTools") {
		input.ReadTools = objectSlice(get(recipe, "readTools"))
	}
	if n, ok := get(recipe, "max").(float64); ok {
		input.EvidenceMaxBytes = &n
	}
	input.ReplySuggestionsEnabled = get(recipe, "suggestions") == true
	if projection := object(get(recipe, "projection")); projection != nil {
		input.Projection = NewContextProjection(str(get(projection, "sceneId")))
		if _, err := input.Projection.Project(get(projection, "seed")); err != nil {
			t.Fatal(err)
		}
	}
	input.Complete = func(ctx context.Context, messages, tools []*Object, settle func(Completion, error)) {
		names := []any{}
		for _, tool := range tools {
			names = append(names, get(tool, "name"))
		}
		events = append(events, obj(member("kind", "complete"), member("messages", clone(objectsValue(messages))), member("tools", names)))
		round++
		if get(recipe, "abortComplete") == float64(round) {
			cancel(errCode("cancelled"))
		}
		if get(recipe, "ineligibleComplete") == float64(round) {
			eligible = false
		}
		rounds := array(get(recipe, "rounds"))
		index := round - 1
		if index >= len(rounds) {
			index = len(rounds) - 1
		}
		calls, assistant := callsFrom(rounds[index])
		settle(Completion{calls, assistant}, nil)
	}
	input.Execute = func(ctx context.Context, name string, args any, settle func(any, error)) {
		events = append(events, obj(member("kind", "execute"), member("name", name), member("args", clone(args))))
		executions++
		if get(recipe, "abortExecute") == float64(executions) {
			cancel(errCode("cancelled"))
		}
		if get(recipe, "ineligibleExecute") == float64(executions) {
			eligible = false
		}
		if message := str(get(recipe, "executeError")); message != "" {
			settle(nil, errCode(message))
			return
		}
		if get(recipe, "executeUndefined") == true {
			settle(Undefined, nil)
			return
		}
		values := array(get(recipe, "values"))
		var value any = []any{}
		if executions <= len(values) && values[executions-1] != nil {
			value = clone(values[executions-1])
		}
		settle(value, nil)
	}
	if mode := str(get(recipe, "deliver")); mode != "" {
		input.Deliver = func(name string, value, args any) (any, error) {
			events = append(events, obj(member("kind", "deliver"), member("name", name), member("value", clone(value)), member("args", clone(args))))
			if mode == "replace" {
				return []any{}, nil
			}
			if mode == "mutate" { // Mutate a shared child: JS push is in-place; Go slices have value headers.
				if a := array(value); len(a) > 0 {
					if o := object(a[0]); o != nil {
						set(o, "mutated", true)
					}
				}
			}
			return value, nil
		}
	}
	if get(recipe, "abortBefore") == true {
		cancel(errCode("cancelled"))
	}
	result, err := p.BuildReplyPlan(ctx, input)
	var value any
	if err == nil {
		value = obj(member("plan", result.Plan), member("evidence", objectsValue(result.Evidence)), member("rounds", float64(result.Rounds)), member("toolCalls", float64(result.ToolCalls)))
	}
	return obj(member("events", events), member("result", captured(value, err)))
}

func TestWithinSignalSourceOrdering(t *testing.T) {
	f := loadOracle(t)
	for _, tc := range f.Signals {
		t.Run(tc.Name, func(t *testing.T) {
			in := parse(t, tc.Input)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			ready := make(chan struct{})
			var settle func(string, error)
			result := make(chan *Object, 1)
			operations := func() {
				for _, op := range memberStrings(get(in, "order")) {
					switch op {
					case "reject":
						settle("", errCode("provider_failed"))
					case "resolve":
						settle("success", nil)
					case "abort":
						cancel(errCode("cancelled"))
					}
				}
			}
			go func() {
				v, err := withinSignal(ctx, func(done func(string, error)) {
					settle = done
					if get(in, "beforeAttach") == true {
						operations()
					}
				}, func() { close(ready) })
				result <- captured(v, err)
			}()
			<-ready
			if get(in, "beforeAttach") != true {
				operations()
			}
			got := encoded(t, <-result)
			if got != tc.Expected {
				t.Fatalf("got %s want %s", got, tc.Expected)
			}
		})
	}
}
