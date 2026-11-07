package topicroute

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func object(members ...jsjson.Member) *jsjson.Object {
	o, err := jsjson.NewObject(members...)
	if err != nil {
		panic(err)
	}
	return o
}
func member(key string, value any) jsjson.Member { return jsjson.Member{Key: key, Value: value} }
func pairs[V any](m *OrderedMap[string, V], value func(V) any) []any {
	out := []any{}
	for _, e := range m.Entries() {
		out = append(out, []any{e.Key, value(e.Value)})
	}
	return out
}
func stateSnapshot(s *RoutingState) *jsjson.Object {
	metadata := pairs(&s.Metadata, func(v *Metadata) any {
		m := object(member("provisional", v.Provisional))
		if v.CueSourceID != nil {
			_ = m.Set("cueSourceId", *v.CueSourceID)
		}
		if v.Cue != nil {
			_ = m.Set("cue", object(member("initiatorId", v.Cue.InitiatorID), member("label", v.Cue.Label)))
		}
		return m
	})
	deferred := pairs(&s.Deferred, func(v Deferred) any { return object(member("schedule", v.Schedule)) })
	stringValue := func(v string) any { return v }
	protected := []any{}
	for _, v := range s.ProtectedSends.Entries() {
		protected = append(protected, v.Key)
	}
	return object(member("overflowed", s.Overflowed), member("metadata", metadata), member("deferred", deferred), member("acknowledged", pairs(&s.Acknowledged, stringValue)), member("protectedSends", protected), member("turns", pairs(&s.Turns, stringValue)))
}
func TestRoutingStateActualNodeTransitions(t *testing.T) {
	data, err := os.ReadFile("testdata/routing-state-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Source, SourceHash string
		Steps              []struct {
			Raw    string
			Result *string
			Digest string
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("../../..", fixture.Source))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(source)) != fixture.SourceHash {
		t.Fatal("source changed: regenerate and review")
	}
	if len(fixture.Steps) < 1000 {
		t.Fatal("source transition inventory shrank")
	}
	var s RoutingState
	for i, step := range fixture.Steps {
		parsed, err := jsjson.Parse(step.Raw)
		if err != nil {
			t.Fatal(err)
		}
		op := parsed.(*jsjson.Object)
		kind := field(op, "kind").(string)
		id, _ := field(op, "id").(string)
		topic, _ := field(op, "topicId").(string)
		var result *string
		switch kind {
		case "defer":
			r := s.Defer(id, field(op, "schedule").(bool))
			result = &r
		case "ack":
			s.Acknowledge(id, topic)
		case "forget":
			s.ForgetMessage(id)
		case "evict":
			s.Evict(id)
		case "cancel":
			s.Cancel()
		case "protect":
			s.ProtectedSends.Set(id, struct{}{})
		case "turn":
			s.Turns.Set(id, topic)
		case "deleteDeferred":
			s.Deferred.Delete(id)
		case "overflow":
			s.Overflowed = field(op, "value").(float64)
		case "metadata":
			v := field(op, "value").(*jsjson.Object)
			cue := field(v, "cue").(*jsjson.Object)
			sourceID := field(v, "cueSourceId").(string)
			s.Metadata.Set(id, &Metadata{field(v, "provisional").(bool), &sourceID, &Cue{field(cue, "initiatorId").(string), field(cue, "label").(string)}})
		default:
			t.Fatal("unsupported oracle operation")
		}
		if (result == nil) != (step.Result == nil) || (result != nil && *result != *step.Result) {
			t.Fatalf("step %d result differs", i)
		}
		raw, err := jsjson.Marshal(stateSnapshot(&s))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != step.Digest {
			t.Fatalf("step %d (%s) state mismatch: %s", i, step.Raw, raw)
		}
	}
}
func TestOrderedMapReplacementAndReinsertion(t *testing.T) {
	var m OrderedMap[string, int]
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("a", 3)
	if e := m.Entries(); len(e) != 2 || e[0].Key != "a" || e[0].Value != 3 {
		t.Fatal(e)
	}
	m.Delete("a")
	m.Set("a", 4)
	if e := m.Entries(); e[0].Key != "b" || e[1].Key != "a" {
		t.Fatal(e)
	}
	m.Clear()
	if m.Len() != 0 {
		t.Fatal("clear")
	}
	m.Set("c", 5)
	if v, ok := m.Get("c"); !ok || v != 5 {
		t.Fatal(v, ok)
	}
}
