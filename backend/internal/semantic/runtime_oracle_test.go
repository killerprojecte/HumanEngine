package semantic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type requestFixtureRouter struct {
	calls   []any
	request providertransport.CompletionRequest
	err     error
}

func (r *requestFixtureRouter) Complete(_ context.Context, q providertransport.CompletionRequest) (*providertransport.Completion, error) {
	r.request = q
	if r.err != nil {
		return nil, r.err
	}
	return &providertransport.Completion{ToolCalls: r.calls}, nil
}
func resolvedObject(o *jsjson.Object) ResolvedInput {
	r := ResolvedInput{Coverage: semString(o, "coverage"), EpisodeAnchor: scObject(scGet(o, "episodeAnchor")), AnchorDependencies: scArr(scGet(o, "anchorDependencies")), AnchorQualifications: scArr(scGet(o, "anchorQualifications"))}
	if v, ok := scGet(o, "partitionKey").(string); ok {
		r.PartitionKey = &v
	}
	if v, ok := scGet(o, "episodeMembershipKey").(string); ok {
		r.EpisodeMembershipKey = &v
	}
	for _, v := range scArr(scGet(o, "entries")) {
		r.Entries = append(r.Entries, InputEntry{scObject(scGet(v, "selection")), scObject(scGet(v, "assertion"))})
	}
	return r
}
func runtimeOracle(t *testing.T) *jsjson.Object {
	t.Helper()
	raw, e := os.ReadFile("testdata/runtime-source.json")
	semMust(t, e)
	o, e := parseRequestObject(string(raw))
	semMust(t, e)
	return o
}
func TestActualSourceSemanticRuntime(t *testing.T) {
	o := runtimeOracle(t)
	for _, m := range scObject(scGet(o, "sources")).Members() {
		raw, e := os.ReadFile(filepath.Join("../../..", m.Key))
		semMust(t, e)
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != m.Value {
			t.Fatalf("stale source %s", m.Key)
		}
	}
	if scGet(o, "schema") != proposalSchemaJSON {
		t.Fatal("proposal schema is not source-exact")
	}
	for _, v := range scArr(scGet(o, "captures")) {
		t.Run(semString(scObject(v), "name"), func(t *testing.T) {
			resolved := resolvedObject(scObject(scGet(v, "resolved")))
			got := CaptureInput("scene", resolved)
			expected := scGet(v, "expected")
			if expected == nil {
				if got != nil {
					t.Fatal("accepted invalid input")
				}
				return
			}
			if got == nil || !privacyEqual(got.Value, expected) {
				g, _ := jsjson.Stringify(nullableInput(got))
				want, _ := jsjson.Stringify(expected)
				t.Fatalf("capture mismatch\ngot %s\nwant %s", g, want)
			}
			if !InputUnchanged(got, resolved) {
				t.Fatal("same input rejected")
			}
			mutated := got.Clone()
			_ = mutated.Value.Set("personId", "changed")
			if InputUnchanged(mutated, resolved) {
				t.Fatal("changed input accepted")
			}
		})
	}
	snapshot := CaptureInput("scene", resolvedObject(scObject(scGet(scArr(scGet(o, "captures"))[0], "resolved"))))
	now, _ := time.Parse(time.RFC3339Nano, "2026-01-02T03:04:05.006Z")
	for _, v := range scArr(scGet(o, "requests")) {
		t.Run("request-"+semString(scObject(v), "name"), func(t *testing.T) {
			router := &requestFixtureRouter{calls: scArr(scGet(v, "toolCalls"))}
			guards := 0
			proposals, e := RequestProposals(context.Background(), snapshot, router, func() error { guards++; return nil }, "UTC", now)
			if expectedErr := scGet(v, "error"); expectedErr != nil {
				if e == nil {
					t.Fatal("source rejected but Go accepted")
				}
				if semString(scObject(v), "name") != "duplicate" && e.Error() != expectedErr {
					t.Fatalf("error %v want %v", e, expectedErr)
				}
			} else {
				semMust(t, e)
				result := []any{}
				for _, p := range proposals {
					result = append(result, p)
				}
				if !privacyEqual(result, scGet(v, "result")) {
					t.Fatal("proposal mismatch")
				}
			}
			if guards != int(scGet(v, "guards").(float64)) {
				t.Fatalf("guards %d", guards)
			}
			wire := scObject(scGet(v, "wire"))
			messages := scArr(scGet(wire, "messages"))
			if router.request.Role != scGet(wire, "role") || router.request.Messages[1].Visible["content"] != scGet(messages[1], "content") {
				t.Fatal("projected request differs")
			}
		})
	}
}
func nullableInput(s *Input) any {
	if s == nil {
		return nil
	}
	return s.Value
}
func TestRequestCancellationAndByteLimit(t *testing.T) {
	o := runtimeOracle(t)
	r := resolvedObject(scObject(scGet(scArr(scGet(o, "captures"))[0], "resolved")))
	snapshot := CaptureInput("scene", r)
	router := &requestFixtureRouter{}
	count := 0
	_, err := RequestProposals(context.Background(), snapshot, router, func() error {
		count++
		if count == 2 {
			return context.Canceled
		}
		return nil
	}, "UTC", time.Unix(0, 0))
	if err != context.Canceled || router.request.Role != "" {
		t.Fatal("cancel did not fence completion")
	}
	record := scObject(scGet(r.Entries[0].Assertion, "record"))
	raw := make([]byte, 70000)
	for i := range raw {
		raw[i] = 'x'
	}
	_ = record.Set("content", string(raw))
	snapshot = CaptureInput("scene", r)
	_, err = RequestProposals(context.Background(), snapshot, router, func() error { return nil }, "UTC", time.Unix(0, 0))
	if err == nil || err.Error() != "semantic_input_byte_limit" {
		t.Fatal(err)
	}
}
func TestProposalNativeMapConversion(t *testing.T) {
	var m map[string]any
	semMust(t, json.Unmarshal([]byte(`{"name":"memory.semantic_propose","arguments":{"kind":"summary_candidate","selectedParentIds":["a","b"],"text":"x","hypothesis":false}}`), &m))
	o, e := requestObject(m)
	semMust(t, e)
	if scObject(scGet(o, "arguments")) == nil {
		t.Fatal("native arguments lost")
	}
}

func TestPhysicalNativeAttemptGuardEvenWithoutAccounting(t *testing.T) {
	o := runtimeOracle(t)
	snapshot := CaptureInput("scene", resolvedObject(scObject(scGet(scArr(scGet(o, "captures"))[0], "resolved"))))
	guards := 0
	_, e := RequestProposals(context.Background(), snapshot, &providertransport.NativeModels{}, func() error {
		guards++
		if guards == 3 {
			return context.Canceled
		}
		return nil
	}, "UTC", time.Unix(0, 0))
	if e != context.Canceled || guards != 3 {
		t.Fatalf("physical boundary missed: %v guards=%d", e, guards)
	}
}
func TestProposalSourceCodePointBounds(t *testing.T) {
	text := ""
	for i := 0; i < 6001; i++ {
		text += "😀"
	}
	_, e := ValidateProposal(inputObject("kind", "summary_candidate", "selectedParentIds", []any{"a", "b"}, "text", text, "hypothesis", false))
	if e == nil {
		t.Fatal("source codepoint text bound exceeded")
	}
}
