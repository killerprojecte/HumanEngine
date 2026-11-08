package experienceservice

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"os"
	"reflect"
	"testing"
)

type domainOracle struct {
	Node       string            `json:"node"`
	Provenance map[string]string `json:"provenance"`
	Candidates []struct {
		Ctx       Context  `json:"ctx"`
		Input     Feedback `json:"input"`
		Outcome   Outcome  `json:"outcome"`
		Candidate Policy   `json:"candidate"`
		Receipt   Receipt  `json:"receipt"`
	} `json:"candidates"`
	Validation []struct {
		Name      string  `json:"name"`
		Candidate Policy  `json:"candidate"`
		Receipt   Receipt `json:"receipt"`
	} `json:"validation"`
	FeedbackCases []struct {
		Message        FeedbackMessage       `json:"message"`
		Target         FeedbackMessage       `json:"target"`
		SelectedPolicy Reference             `json:"selectedPolicy"`
		AppliedPolicy  AppliedPolicyFeedback `json:"appliedPolicy"`
		Result         *Feedback             `json:"result"`
	} `json:"feedbackCases"`
	Behaviors []BehaviorFixture `json:"behaviors"`
}

func loadDomainOracle(t *testing.T) domainOracle {
	t.Helper()
	b, e := os.ReadFile("testdata/domain-oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	var v domainOracle
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	if v.Node != "v24.22.1" || len(v.Provenance) != 8 {
		t.Fatalf("bad source provenance: %+v", v.Provenance)
	}
	return v
}
func equalDomainJSON(t *testing.T, a, b any) {
	t.Helper()
	ab, e := json.Marshal(a)
	if e != nil {
		t.Fatal(e)
	}
	bb, e := json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	var av, bv any
	if e = json.Unmarshal(ab, &av); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(bb, &bv); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(av, bv) {
		t.Fatalf("domain mismatch\ngot %s\nwant %s", ab, bb)
	}
}
func TestPinnedSourceDomainOracle(t *testing.T) {
	o := loadDomainOracle(t)
	for _, c := range o.Candidates {
		t.Run(c.Input.Code, func(t *testing.T) {
			got, e := CreateOutcome(c.Ctx, c.Input, c.Outcome.ID)
			if e != nil {
				t.Fatal(e)
			}
			equalDomainJSON(t, got, c.Outcome)
			p, e := CandidateFromOutcome(*got, c.Candidate.ID, 1767225600000)
			if e != nil {
				t.Fatal(e)
			}
			equalDomainJSON(t, p, c.Candidate)
			if p.Hash != c.Candidate.Hash {
				t.Fatal("semantic hash drift")
			}
			receipt := ValidateExperiencePolicy(*p)
			equalDomainJSON(t, receipt, c.Receipt)
			if !ValidMechanicalReceipt(*p, receipt) {
				t.Fatal("valid source receipt rejected")
			}
		})
	}
	for _, c := range o.Validation {
		t.Run(c.Name, func(t *testing.T) { equalDomainJSON(t, ValidateExperiencePolicy(c.Candidate), c.Receipt) })
	}
}
func TestPinnedSourceFeedbackOracle(t *testing.T) {
	o := loadDomainOracle(t)
	for _, c := range o.FeedbackCases {
		t.Run(c.Message.Parts[0].Text, func(t *testing.T) {
			got := DeriveExplicitPolicyFeedback(c.Message.SceneID, c.Message, &c.Target, &c.SelectedPolicy, &c.AppliedPolicy)
			equalDomainJSON(t, got, c.Result)
		})
	}
}
func TestPinnedSourceOperationBehaviorOracle(t *testing.T) {
	o := loadDomainOracle(t)
	for _, c := range o.Behaviors {
		t.Run(c.ID, func(t *testing.T) {
			if got := AppliedExperienceOperation(c.Input); got != c.Expected {
				t.Fatalf("got %q want %q", got, c.Expected)
			}
		})
	}
}
func TestDomainCloneAndLosslessSourceRefs(t *testing.T) {
	ref := &sourceevents.Ref{EventID: "event", Revision: 3, DeletionEpoch: 0}
	lone := jsjson.FromUTF16([]uint16{0xd800})
	f := Feedback{Source: Source{MessageID: lone, SenderID: "person", EventRef: ref}, Target: Target{MessageID: "target", EventRef: ref}, Code: "request_conclusion_first", ObservedAt: "2026-02-30T00:00:00Z"}
	o, e := CreateOutcome(Context{SceneID: "s"}, f, "o")
	if e != nil {
		t.Fatal("V8 overflow day should be accepted:", e)
	}
	ref.Revision = 4
	if o.Source.EventRef.Revision != 3 || o.Target.EventRef.Revision != 3 {
		t.Fatal("feedback did not clone refs")
	}
	b, e := json.Marshal(o)
	if e != nil {
		t.Fatal(e)
	}
	var restored Outcome
	if e = json.Unmarshal(b, &restored); e != nil {
		t.Fatal(e)
	}
	if restored.Source.MessageID != lone || restored.Source.EventRef.EventID != "event" || restored.Source.EventRef.Revision != 3 {
		t.Fatalf("source ref/string changed: %s", b)
	}
}
func TestFeedbackIsNotSentimentOrACKAuthority(t *testing.T) {
	o := loadDomainOracle(t)
	c := o.FeedbackCases[0]
	for _, change := range []func(*FeedbackMessage){func(m *FeedbackMessage) { m.Origin = "history" }, func(m *FeedbackMessage) { m.Status = "recalled" }, func(m *FeedbackMessage) { m.ReplyToID = "other" }, func(m *FeedbackMessage) { m.SourceEventRegistrationLimited = true }, func(m *FeedbackMessage) { m.ReceivedAt = "2025-01-01T00:00:00Z" }, func(m *FeedbackMessage) { m.Parts = []FeedbackPart{{Type: "image"}} }} {
		m := c.Message
		change(&m)
		if DeriveExplicitPolicyFeedback(m.SceneID, m, &c.Target, &c.SelectedPolicy, &c.AppliedPolicy) != nil {
			t.Fatal("unauthorized feedback accepted")
		}
	}
}
func TestMechanicalReceiptCannotClaimEffectiveness(t *testing.T) {
	o := loadDomainOracle(t)
	p := o.Candidates[0].Candidate
	r := ValidateExperiencePolicy(p)
	r.HeldoutRoots = append(r.HeldoutRoots, p.GenerationRoots[0])
	if ValidMechanicalReceipt(p, r) {
		t.Fatal("lineage overlap accepted")
	}
	r = ValidateExperiencePolicy(p)
	r.CandidateVersion++
	if ValidMechanicalReceipt(p, r) {
		t.Fatal("wrong version accepted")
	}
	p.CounterexampleIDs = []string{"counterexample"}
	p.Hash = CandidateHash(p)
	if ValidateExperiencePolicy(p).Passed {
		t.Fatal("counterexample accepted")
	}
}

func TestQualificationMaterialKeepsUnknownProperties(t *testing.T) {
	raw := []byte(`{"source":{"messageId":"s","senderId":"p","extra":"source-extra"},"target":{"messageId":"t"},"code":"request_conclusion_first","observedAt":"2026-01-01T00:00:00Z","id":"o","version":1,"rootId":"r","rootFamily":"f","sceneId":"scene","z":{"ä":"\ud800"}}`)
	var o Outcome
	if e := json.Unmarshal(raw, &o); e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(o)
	if e != nil {
		t.Fatal(e)
	}
	v, e := jsjson.Parse(string(b))
	if e != nil {
		t.Fatal(e)
	}
	obj := v.(*jsjson.Object)
	z, ok := getValue(obj, "z").(*jsjson.Object)
	if !ok || getValue(z, "ä") != jsjson.FromUTF16([]uint16{0xd800}) {
		t.Fatalf("unknown qualification evidence lost: %s", b)
	}
	source := getValue(obj, "source").(*jsjson.Object)
	if getValue(source, "extra") != "source-extra" {
		t.Fatalf("unknown source evidence lost: %s", b)
	}
	receiptJSON := []byte(`{"candidateId":"p","candidateVersion":1,"candidateHash":"h","tier":"mechanical_only","ruleVersion":"r","ruleHash":"h","datasetVersion":"d","datasetHash":"h","generationRoots":[],"generationFamilies":[],"heldoutRoots":[],"heldoutFamilies":[],"checks":[],"failures":[],"passed":true,"extra":{"x":1}}`)
	var r Receipt
	if e = json.Unmarshal(receiptJSON, &r); e != nil {
		t.Fatal(e)
	}
	b, e = json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	v, _ = jsjson.Parse(string(b))
	if getValue(v.(*jsjson.Object), "extra") == nil {
		t.Fatal("unknown receipt property lost")
	}
}
