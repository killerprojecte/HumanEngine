package replyplan

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
)

type optionalEmojiOracle struct {
	FrozenExpected string        `json:"frozenExpected"`
	Cases          []fixtureCase `json:"cases"`
	Handoff        fixtureCase   `json:"handoff"`
}

func loadOptionalEmojiOracle(t *testing.T) optionalEmojiOracle {
	t.Helper()
	data, err := os.ReadFile("testdata/optional-emoji.json")
	if err != nil {
		t.Fatal(err)
	}
	var delta optionalEmojiOracle
	if err := json.Unmarshal(data, &delta); err != nil {
		t.Fatal(err)
	}
	return delta
}

// Preserve the original rejection as historical evidence, and require its exact
// input to match the separately captured source delta before changing expectation.
func applyOptionalEmojiDelta(t *testing.T, frozen *sourceOracle) {
	t.Helper()
	delta := loadOptionalEmojiOracle(t)
	found := false
	for i, tc := range frozen.Cases {
		if tc.Name != "required-emoji" {
			continue
		}
		if len(delta.Cases) == 0 || delta.Cases[0].Name != tc.Name || delta.Cases[0].Input != tc.Input || tc.Expected != delta.FrozenExpected {
			t.Fatal("optional emoji delta does not match frozen source case")
		}
		frozen.Cases[i].Expected = delta.Cases[0].Expected
		found = true
	}
	if !found {
		t.Fatal("frozen required-emoji case missing")
	}
}

func TestOptionalEmojiSourceDelta(t *testing.T) {
	delta := loadOptionalEmojiOracle(t)
	dates, err := jsdate.New("UTC")
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(fixtureComparator(loadOracle(t)), dates)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range delta.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			in := parse(t, tc.Input)
			value, err := p.ValidateReplyPlan(get(in, "value"), validationInput(get(in, "input")))
			if got := encoded(t, captured(value, err)); got != tc.Expected {
				t.Fatalf("got %s\nwant %s", got, tc.Expected)
			}
		})
	}
	t.Run("planner-to-replyer-brief", func(t *testing.T) {
		in := parse(t, delta.Handoff.Input)
		completions := 0
		result, err := p.BuildReplyPlan(context.Background(), BuildInput{
			ValidationInput: validationInput(get(in, "input")),
			Eligible:        func() bool { return true },
			Complete: func(_ context.Context, messages, tools []*Object, settle func(Completion, error)) {
				completions++
				if completions != 1 {
					t.Fatal("omitted emoji incorrectly invokes repair")
				}
				if len(tools) != 1 || get(tools[0], "name") != "reply.plan" {
					t.Fatal("unexpected tool contract")
				}
				if includes(memberStrings(get(get(tools[0], "inputSchema"), "required")), "emoji") {
					t.Fatal("optional emoji advertised as required")
				}
				settle(Completion{ToolCalls: []ToolCall{{ID: "plan", Name: "reply.plan", Arguments: get(in, "value")}}, Assistant: obj(member("role", "assistant"), member("content", ""))}, nil)
			},
			Execute: func(_ context.Context, _ string, _ any, _ func(any, error)) { t.Fatal("unexpected tool execution") },
		})
		if err != nil {
			t.Fatal(err)
		}
		brief, err := p.BuildReplyBrief(result.Plan, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		value := obj(member("result", obj(member("plan", result.Plan), member("evidence", objectsValue(result.Evidence)), member("rounds", float64(result.Rounds)), member("toolCalls", float64(result.ToolCalls)))), member("brief", brief), member("completions", float64(completions)))
		if got := encoded(t, value); got != delta.Handoff.Expected {
			t.Fatalf("got %s\nwant %s", got, delta.Handoff.Expected)
		}
	})
}
