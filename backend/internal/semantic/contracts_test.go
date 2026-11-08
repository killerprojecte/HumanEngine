package semantic

import (
	"math"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

const contractHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const contractProof = `{"proofId":"proof","sources":[{"sceneId":"scene","messageId":"message","senderId":"sender","eventRef":{"eventId":"event","revision":1,"deletionEpoch":0}}],"parents":["ancestor"],"parentReferences":[{"id":"ancestor","version":1,"deletionEpoch":0}],"subjectWitness":{"kind":"self","personId":"person","sourceMessageId":"message","mentionPartIndex":0},"sourceObservations":[{"sceneId":"scene","messageId":"message","senderId":"sender","eventRef":{"eventId":"event","revision":1,"deletionEpoch":0},"sentAt":"2024-02-29T01:02:03.123+23:59","receivedAt":"2024-02-29T01:02:03Z"}],"invalidatedBy":"other"}`

func contractParent(id string) string {
	return `{"ref":{"id":"` + id + `","version":1,"deletionEpoch":0},"dependencyKey":"` + contractHash + `","fingerprint":"` + contractHash + `","proofs":[` + contractProof + `],"roots":["` + contractHash + `"]}`
}
func contractCandidateRaw() string {
	return `{"schemaVersion":1,"id":"candidate","sceneId":"scene","personId":null,"partitionKey":"` + contractHash + `","version":1,"inputKey":"` + contractHash + `","kind":"summary_candidate","text":"Synthetic summary","selectedParentIds":["parent-a","parent-b"],"parents":[` + contractParent("parent-a") + `,` + contractParent("parent-b") + `],"anchorDependencies":[],"anchorQualifications":[],"hypothesis":false,"status":"candidate","admission":"qualified_candidate","createdAt":"2024-02-29T01:02:03Z","applicableUntil":null}`
}
func contractFixture(t *testing.T) *jsjson.Object {
	t.Helper()
	o, e := ParseCandidate(contractCandidateRaw())
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func contractFirstProof(o *jsjson.Object) *jsjson.Object {
	return scObject(scArr(scGet(scArr(scGet(o, "parents"))[0], "proofs"))[0])
}
func contractReverse(v any) any {
	switch x := v.(type) {
	case *jsjson.Object:
		out := new(jsjson.Object)
		members := x.Members()
		for i := len(members) - 1; i >= 0; i-- {
			_ = out.Set(members[i].Key, contractReverse(members[i].Value))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = contractReverse(v)
		}
		return out
	default:
		return x
	}
}
func TestContractSourceOrder(t *testing.T) {
	input := contractReverse(contractFixture(t)).(*jsjson.Object)
	o, e := ValidateCandidate(input)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := jsjson.Stringify(o)
	if e != nil {
		t.Fatal(e)
	}
	if raw != contractCandidateRaw() {
		t.Fatalf("schema order differs\n%s", raw)
	}
	// Canonicalization produces independent nested objects, not aliases.
	_ = contractFirstProof(o).Set("proofId", "changed")
	if scGet(contractFirstProof(input), "proofId") != "proof" {
		t.Fatal("canonicalization aliases caller objects")
	}
	attemptRaw := `{"schemaVersion":1,"sceneId":"scene","inputKey":"` + contractHash + `","status":"started","version":1,"parents":[{"ref":{"id":"parent","version":1,"deletionEpoch":0},"dependencyKey":"` + contractHash + `"}],"roots":["` + contractHash + `"]}`
	a, e := ParseAttempt(attemptRaw)
	if e != nil {
		t.Fatal(e)
	}
	a, e = ValidateAttempt(contractReverse(a).(*jsjson.Object))
	if e != nil {
		t.Fatal(e)
	}
	raw, e = jsjson.Stringify(a)
	if e != nil || raw != attemptRaw {
		t.Fatalf("attempt order: %s %v", raw, e)
	}
}
func TestContractRejectsUnknownNestedFields(t *testing.T) {
	cases := map[string]func(*jsjson.Object) *jsjson.Object{
		"candidate": func(o *jsjson.Object) *jsjson.Object { return o },
		"parent":    func(o *jsjson.Object) *jsjson.Object { return scObject(scArr(scGet(o, "parents"))[0]) },
		"reference": func(o *jsjson.Object) *jsjson.Object { return scObject(scGet(scArr(scGet(o, "parents"))[0], "ref")) },
		"proof":     contractFirstProof,
		"source": func(o *jsjson.Object) *jsjson.Object {
			return scObject(scArr(scGet(contractFirstProof(o), "sources"))[0])
		},
		"event": func(o *jsjson.Object) *jsjson.Object {
			return scObject(scGet(scArr(scGet(contractFirstProof(o), "sources"))[0], "eventRef"))
		},
		"observation": func(o *jsjson.Object) *jsjson.Object {
			return scObject(scArr(scGet(contractFirstProof(o), "sourceObservations"))[0])
		},
		"witness": func(o *jsjson.Object) *jsjson.Object { return scObject(scGet(contractFirstProof(o), "subjectWitness")) },
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			o := contractFixture(t)
			_ = target(o).Set("unexpected", true)
			if _, e := ValidateCandidate(o); e == nil {
				t.Fatal("accepted unknown key")
			}
		})
	}
}
func TestContractProofBindingsAndOptionalNull(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*jsjson.Object)
		valid  bool
	}{
		{"legacy parents", func(p *jsjson.Object) { p.Delete("parentReferences") }, true},
		{"matching empty", func(p *jsjson.Object) { _ = p.Set("parents", []any{}); _ = p.Set("parentReferences", []any{}) }, true},
		{"duplicate parent", func(p *jsjson.Object) { _ = p.Set("parents", []any{"ancestor", "ancestor"}) }, false},
		{"missing binding", func(p *jsjson.Object) { _ = p.Set("parentReferences", []any{}) }, false},
		{"foreign binding", func(p *jsjson.Object) { _ = scObject(scArr(scGet(p, "parentReferences"))[0]).Set("id", "foreign") }, false},
		{"null bindings", func(p *jsjson.Object) { _ = p.Set("parentReferences", nil) }, false},
		{"null witness", func(p *jsjson.Object) { _ = p.Set("subjectWitness", nil) }, false},
		{"null observations", func(p *jsjson.Object) { _ = p.Set("sourceObservations", nil) }, false},
		{"null event", func(p *jsjson.Object) { _ = scObject(scArr(scGet(p, "sources"))[0]).Set("eventRef", nil) }, false},
		{"omitted event", func(p *jsjson.Object) { scObject(scArr(scGet(p, "sources"))[0]).Delete("eventRef") }, true},
		{"null observation time", func(p *jsjson.Object) { _ = scObject(scArr(scGet(p, "sourceObservations"))[0]).Set("sentAt", nil) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := contractFixture(t)
			tc.mutate(contractFirstProof(o))
			_, e := ValidateCandidate(o)
			if (e == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, e)
			}
		})
	}
	o := contractFixture(t)
	o.Delete("personId")
	if _, e := ValidateCandidate(o); e == nil {
		t.Fatal("required nullable field may not be omitted")
	}
	o = contractFixture(t)
	_ = o.Set("episodeAnchor", nil)
	if _, e := ValidateCandidate(o); e == nil {
		t.Fatal("optional anchor may not be null")
	}
}
func contractEpisode(t *testing.T) *jsjson.Object {
	t.Helper()
	o := contractFixture(t)
	_ = o.Set("kind", "episode_candidate")
	_ = o.Set("partitionKey", nil)
	anchor, e := jsjson.Parse(`{"id":"anchor","version":1}`)
	if e != nil {
		t.Fatal(e)
	}
	_ = o.Set("episodeAnchor", anchor)
	_ = o.Set("episodeMembershipKey", contractHash)
	dependency, e := jsjson.Parse(`{"ref":{"id":"backing","version":1,"deletionEpoch":0},"fingerprint":"` + contractHash + `","proofs":[` + contractProof + `]}`)
	if e != nil {
		t.Fatal(e)
	}
	_ = o.Set("anchorDependencies", []any{dependency})
	qualification, e := jsjson.Parse(`{"edgeId":"edge","edgeVersion":1,"registryVersion":1,"predicate":"has_property","property":"enabled","subject":{"kind":"person","personId":"person"},"object":{"kind":"literal","valueType":"boolean","value":true},"polarity":"positive","modality":"asserted","temporalScope":"current","conditions":[],"occurredAt":null,"validFrom":null,"validUntil":null,"status":"candidate","admission":"qualified_candidate","interpretation":"attributed_proposed_association_not_observed_event"}`)
	if e != nil {
		t.Fatal(e)
	}
	_ = o.Set("anchorQualifications", []any{qualification})
	return o
}
func TestContractEpisodeConstraints(t *testing.T) {
	if _, e := ValidateCandidate(contractEpisode(t)); e != nil {
		t.Fatal(e)
	}
	cases := map[string]func(*jsjson.Object){
		"partition":      func(o *jsjson.Object) { _ = o.Set("partitionKey", contractHash) },
		"anchor":         func(o *jsjson.Object) { o.Delete("episodeAnchor") },
		"membership":     func(o *jsjson.Object) { o.Delete("episodeMembershipKey") },
		"dependencies":   func(o *jsjson.Object) { _ = o.Set("anchorDependencies", []any{}) },
		"qualifications": func(o *jsjson.Object) { _ = o.Set("anchorQualifications", []any{}) },
		"duplicate edge": func(o *jsjson.Object) {
			q := scArr(scGet(o, "anchorQualifications"))[0]
			_ = o.Set("anchorQualifications", []any{q, q})
		},
		"unknown qualification": func(o *jsjson.Object) {
			_ = scObject(scArr(scGet(o, "anchorQualifications"))[0]).Set("unexpected", true)
		},
		"unknown endpoint": func(o *jsjson.Object) {
			_ = scObject(scGet(scArr(scGet(o, "anchorQualifications"))[0], "subject")).Set("unexpected", true)
		},
		"duplicate selected parent": func(o *jsjson.Object) { _ = o.Set("selectedParentIds", []any{"parent-a", "parent-a"}) },
		"unknown selected parent":   func(o *jsjson.Object) { _ = o.Set("selectedParentIds", []any{"parent-a", "missing"}) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := contractEpisode(t)
			mutate(o)
			if _, e := ValidateCandidate(o); e == nil {
				t.Fatal("accepted invalid episode")
			}
		})
	}
	// Anchor qualifications use proposal field schemas, not proposal-level refinements.
	o := contractEpisode(t)
	q := scObject(scArr(scGet(o, "anchorQualifications"))[0])
	q.Delete("property")
	_ = q.Set("validFrom", "2025-01-01T00:00:00Z")
	_ = q.Set("validUntil", "2024-01-01T00:00:00Z")
	if _, e := ValidateCandidate(o); e != nil {
		t.Fatalf("invented proposal-level refinement: %v", e)
	}
}
func TestContractSafeNumbersAndUnicodeLimits(t *testing.T) {
	for _, n := range []float64{0, -1, 1.5, 9007199254740992, math.Inf(1), math.NaN()} {
		o := contractFixture(t)
		_ = o.Set("version", n)
		if _, e := ValidateCandidate(o); e == nil {
			t.Fatalf("accepted version %v", n)
		}
	}
	o := contractFixture(t)
	_ = o.Set("version", float64(9007199254740991))
	_ = o.Set("id", strings.Repeat("😀", 512))
	if _, e := ValidateCandidate(o); e != nil {
		t.Fatalf("source boundaries rejected: %v", e)
	}
	_ = o.Set("id", strings.Repeat("😀", 513))
	if _, e := ValidateCandidate(o); e == nil {
		t.Fatal("code point maximum ignored")
	}
	o = contractFixture(t)
	_ = scObject(scArr(scGet(contractFirstProof(o), "parentReferences"))[0]).Set("deletionEpoch", float64(9007199254740992))
	if _, e := ValidateCandidate(o); e == nil {
		t.Fatal("nested safe integer overflow accepted")
	}
	o = contractFixture(t)
	_ = o.Set("createdAt", "2023-02-29T00:00:00Z")
	if _, e := ValidateCandidate(o); e == nil {
		t.Fatal("invalid calendar date accepted")
	}
}
