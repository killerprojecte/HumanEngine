package experienceservice

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"testing"
	"time"
)

func TestRuntimeOutcomeViewActualSource(t *testing.T) {
	raw, e := os.ReadFile("testdata/runtime-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Cases []struct {
			Name string
			View json.RawMessage
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	var outcome Outcome
	input := `{"id":"o","version":1,"sceneId":"s","rootId":"r","rootFamily":"f","source":{"messageId":"m","senderId":"alice","eventRef":null},"target":{"messageId":"b","turnId":"t","eventRef":null},"code":"request_exact_person_lookup","observedAt":"2026-10-29T00:00:00.000Z"}`
	if e = json.Unmarshal([]byte(input), &outcome); e != nil {
		t.Fatal(e)
	}
	expected, e := jsjson.Parse(string(fixture.Cases[0].View))
	if e != nil {
		t.Fatal(e)
	}
	if !deepRuntimeEqual(outcomeView(outcome), expected) {
		t.Fatalf("source outcome projection got %s want %s", encoded(outcomeView(outcome)), fixture.Cases[0].View)
	}
	a := object("a", float64(1), "b", object("nested", true))
	b := object("b", object("nested", true), "a", float64(1))
	if !deepRuntimeEqual(a, b) {
		t.Fatal("object insertion order changed equality")
	}
	_ = b.Set("extra", nil)
	if deepRuntimeEqual(a, b) {
		t.Fatal("extra null property lost")
	}
}

func TestRuntimeExtensionRequiresGatesAndMergesTools(t *testing.T) {
	r := &RuntimeHints{}
	if _, _, e := r.ComposeExtension(botruntime.Extension{}, RuntimeGates{}); e == nil {
		t.Fatal("permissive missing gates")
	}
	person := runtimeTool("memory.person", "read")
	base := botruntime.Extension{Tools: []*jsjson.Object{person, runtimeTool("experience.outcomes", "read"), runtimeTool("experience.propose", "write")}}
	g := RuntimeGates{Tools: []*jsjson.Object{runtimeTool("experience.outcomes", "read"), runtimeTool("experience.propose", "write")}, Planner: func(_ *botruntime.TurnContext, run func() (any, error)) (any, error) { return run() }, Tool: func(string, *botruntime.TurnContext) error { return nil }, Proposal: func(*botruntime.TurnContext) (string, error) { return "", nil }, Report: func(string, error) {}}
	ext, _, e := r.ComposeExtension(base, g)
	if e != nil {
		t.Fatal(e)
	}
	if len(ext.Tools) != 3 || ext.Tools[0] != person || ext.Tools[1] != g.Tools[0] || ext.Tools[2] != g.Tools[1] {
		t.Fatal("tools duplicated or another domain changed")
	}
	if len(base.Tools) != 3 {
		t.Fatal("mutated base")
	}
}

func TestRuntimeProposalActualZodCodePointBoundaries(t *testing.T) {
	raw, e := os.ReadFile("testdata/runtime-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Proposals []struct {
			Name     string
			Input    json.RawMessage
			Accepted bool
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	if len(fixture.Proposals) != 8 {
		t.Fatal("missing source schema boundary cases")
	}
	for _, tc := range fixture.Proposals {
		t.Run(tc.Name, func(t *testing.T) {
			input, e := jsjson.Parse(string(tc.Input))
			if e != nil {
				t.Fatal(e)
			}
			_, e = parseRuntimeProposal(input)
			if (e == nil) != tc.Accepted {
				t.Fatalf("accepted=%v source=%v error=%v", e == nil, tc.Accepted, e)
			}
		})
	}
}

func TestRuntimeSentBarrierPanicAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := make(chan error, 1)
	binding := &RuntimeBinding{ctx: ctx, cancel: cancel, report: func(_ string, e error) { reports <- e }}
	after := make(chan struct{})
	binding.enqueue(func() { panic("synthetic association failure") })
	binding.enqueue(func() { close(after) })
	if e := binding.Barrier(); e != nil {
		t.Fatal(e)
	}
	select {
	case <-after:
	default:
		t.Fatal("panic stranded queue")
	}
	select {
	case <-reports:
	default:
		t.Fatal("panic was not reported")
	}
	blocked, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	binding.enqueue(func() { close(blocked); <-release })
	<-blocked
	done := make(chan error, 1)
	go func() { done <- binding.Barrier() }()
	binding.Close()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("close stranded ingress barrier")
	}
}
