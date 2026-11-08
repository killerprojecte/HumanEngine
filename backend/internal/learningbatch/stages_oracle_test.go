package learningbatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type stageReads struct {
	fixtureReads
	target bool
}

func (f stageReads) UnsupportedPronouns(_ context.Context, _ string, c *Object, _ []any) (bool, error) {
	return strings.HasPrefix(str(get(c, "content")), "She "), nil
}
func (f stageReads) Targets(context.Context, string, []*Object, []*Object, []any) (TargetScope, error) {
	targets := []*Object{}
	if f.target {
		targets = append(targets, obj("id", "memory", "content", "Synthetic durable fact", "kind", "fact", "scope", obj("kind", "scene", "sceneId", "s"), "origin", "learned", "status", "active", "lineageStatus", "verified", "locked", false, "canRevise", true, "candidateIndexes", []any{float64(0)}, "learnedAt", "2026-01-01T00:00:00.000Z"))
	}
	return TargetScope{targets, "complete", obj("perCandidate", float64(4), "perGroup", float64(8), "total", float64(16), "semantic", float64(8))}, nil
}
func TestActualSourceStageRequestsAndCandidateOrder(t *testing.T) {
	raw, e := os.ReadFile("testdata/stages.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Sources map[string]string
		Cases   []struct {
			Plan struct {
				Name      string
				Flags     int
				Target    bool
				Responses [][]map[string]any
			}
			Messages json.RawMessage
			Flow     []map[string]any
			Requests []struct {
				Role            string
				Messages, Tools []map[string]any
			}
			Writes []struct {
				Kind  string
				Input json.RawMessage
			}
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	for p, h := range fixture.Sources {
		b, e := os.ReadFile(filepath.Join("../../..", p))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != h {
			t.Fatal("source changed", p)
		}
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Plan.Name, func(t *testing.T) {
			b, f := persistenceFixture(t)
			b.r.d.Reads = stageReads{fixtureReads{f: f}, tc.Plan.Target}
			b.settings = Settings{Timezone: "UTC", GraphEnabled: tc.Plan.Flags&1 != 0, CommitmentsEnabled: tc.Plan.Flags&2 != 0, KnowledgeEnabled: tc.Plan.Flags&4 != 0}
			b.r.d.Now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 6000000, time.UTC) }
			b.projection = replyplan.NewContextProjection("s")
			flow := []any{}
			b.r.d.Event = func(e FlowEvent) { flow = append(flow, flowProjection(e)) }
			v, e := jsjson.ParseBytes(tc.Messages)
			if e != nil {
				t.Fatal(e)
			}
			b.messages = nil
			for _, m := range v.([]any) {
				b.messages = append(b.messages, m.(*Object))
			}
			b.evidence, e = sourceadmission.TrustedEvidence(b.messages, func(string, string) (bool, error) { return false, nil })
			if e != nil {
				t.Fatal(e)
			}
			count := 0
			b.r.d.Models = syntheticModels(t, func(actual map[string]any) map[string]any {
				index := count
				count++
				if index >= len(tc.Requests) {
					t.Errorf("unexpected model call %d", index)
					return responseCalls()
				}
				want := tc.Requests[index]
				expected, e := providerwire.BuildRequest("openai-chat", "synthetic", want.Messages, want.Tools, false, false)
				if e != nil {
					t.Error(e)
				}
				for _, key := range []string{"messages", "tools"} {
					aa, _ := json.Marshal(actual[key])
					ee, _ := json.Marshal(expected[key])
					if string(aa) != string(ee) {
						t.Errorf("request %d %s mismatch\ngot %s\nwant %s", index, key, aa, ee)
					}
				}
				return responseCalls(tc.Plan.Responses[index]...)
			})
			calls, extra, e := b.extract()
			if e != nil {
				t.Fatal(e)
			}
			if e = b.repair(calls, extra); e != nil {
				t.Fatal(e)
			}
			if e = b.reconcile(calls, nil); e != nil {
				t.Fatal(e)
			}
			wantFlow := []any{}
			for _, event := range tc.Flow {
				if event["stage"] != "learning.persist" {
					wantFlow = append(wantFlow, event)
				}
			}
			if !reflect.DeepEqual(normalizeFlow(flow), normalizeFlow(wantFlow)) {
				a, _ := json.Marshal(flow)
				b, _ := json.Marshal(wantFlow)
				t.Fatalf("source flows %s != %s", a, b)
			}
			if count != len(tc.Requests) {
				t.Fatalf("calls %d expected %d", count, len(tc.Requests))
			}
			inputs := []any{}
			for _, name := range []string{"habits.propose", "learning.propose"} {
				for _, c := range calls {
					if c.Name != name {
						continue
					}
					schema := candidateName(b.settings)
					if name == "habits.propose" {
						schema = "habit"
					}
					v, e := b.r.schemas.parse(c.Arguments, schema)
					if e == nil {
						inputs = append(inputs, native(v))
					}
				}
			}
			expected := []any{}
			for _, w := range tc.Writes {
				v, e := jsjson.ParseBytes(w.Input)
				if e != nil {
					t.Fatal(e)
				}
				expected = append(expected, native(v))
			}
			if !reflect.DeepEqual(inputs, expected) {
				a, _ := json.Marshal(inputs)
				b, _ := json.Marshal(expected)
				t.Fatalf("final source candidates: %s != %s", a, b)
			}
		})
	}
}
func TestReconciliationUsesJavaScriptWhitespaceAndCase(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		equal bool
	}{{"\ufeffSYNTHETIC\u00a0FACT\ufeff", "synthetic fact", true}, {"x\u0085y", "x y", false}, {"ΟΣ", "ος", true}} {
		if got := normalized(c.a) == normalized(c.b); got != c.equal {
			t.Fatalf("normalization %q %q: %v", c.a, c.b, got)
		}
	}
}
