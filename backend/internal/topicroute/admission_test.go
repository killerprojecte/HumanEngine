package topicroute

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"golang.org/x/text/unicode/norm"
)

func admissionNumber(v any) float64 {
	if n, ok := v.(float64); ok {
		return n
	}
	switch field(v.(*jsjson.Object), "$number") {
	case "NaN":
		return math.NaN()
	case "Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	panic("invalid fixture number")
}
func admissionString(t *testing.T, raw string) string {
	t.Helper()
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v.(string)
}
func admissionDecode(t *testing.T, raw string) (TopicInput, []TopicSnapshot, AdmissionConfig) {
	t.Helper()
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	root := v.(*jsjson.Object)
	i := field(root, "input").(*jsjson.Object)
	input := TopicInput{SceneID: field(i, "sceneId").(string), SenderID: field(i, "senderId").(string), Now: admissionNumber(field(i, "now"))}
	if v, ok := i.Get("cue"); ok {
		s := v.(string)
		input.Cue = &s
	}
	if v, ok := i.Get("unavailableReply"); ok {
		input.UnavailableReply = v.(bool)
	}
	if v, ok := i.Get("anchors"); ok {
		for _, a := range v.([]any) {
			o := a.(*jsjson.Object)
			input.Anchors = append(input.Anchors, TopicAnchor{field(o, "kind").(string), field(o, "topicId").(string)})
		}
	}
	topics := []TopicSnapshot{}
	for _, v := range field(root, "topics").([]any) {
		o := v.(*jsjson.Object)
		s := TopicSnapshot{ID: field(o, "id").(string), SceneID: field(o, "sceneId").(string), TouchedAt: admissionNumber(field(o, "touchedAt")), Participants: stringsOf(field(o, "participants")), Provisional: field(o, "provisional").(bool), Active: field(o, "active").(bool), Pending: field(o, "pending").(bool), Waiting: field(o, "waiting").(bool), SendState: field(o, "sendState").(string)}
		if v, ok := o.Get("cue"); ok {
			c := v.(*jsjson.Object)
			s.Cue = &Cue{field(c, "initiatorId").(string), field(c, "label").(string)}
		}
		topics = append(topics, s)
	}
	c := field(root, "config").(*jsjson.Object)
	config := AdmissionConfig{}
	if v, ok := c.Get("maxSlots"); ok {
		n := admissionNumber(v)
		config.MaxSlots = &n
	}
	if v, ok := c.Get("recentMs"); ok {
		n := admissionNumber(v)
		config.RecentMS = &n
	}
	return input, topics, config
}
func admissionExpected(t *testing.T, raw string) TopicAssignment {
	t.Helper()
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*jsjson.Object)
	a := TopicAssignment{Kind: field(o, "kind").(string), Reason: field(o, "reason").(string), CandidateIDs: stringsOf(field(o, "candidateIds"))}
	if v, ok := o.Get("topicId"); ok {
		s := v.(string)
		a.TopicID = &s
	}
	if v, ok := o.Get("evictTopicId"); ok {
		s := v.(string)
		a.EvictTopicID = &s
	}
	if v, ok := o.Get("cue"); ok {
		c := v.(*jsjson.Object)
		a.Cue = &Cue{field(c, "initiatorId").(string), field(c, "label").(string)}
	}
	return a
}
func TestAdmissionActualNodeOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/admission-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Source, SourceHash, Node, ICU, Locale string
		Cases                                 []struct{ Name, Raw, Result, Error string }
		Parses                                []struct {
			Raw    string
			Result *string
		}
		Normalizations []struct{ Raw, Result string }
		Pairs          []struct {
			A, B   string
			Result int
		}
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("../../..", f.Source))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(source)) != f.SourceHash {
		t.Fatal("source changed: regenerate and review oracle")
	}
	if norm.Version != "17.0.0" {
		t.Fatal("normalization Unicode version changed")
	}
	if f.Node != "v24.22.1" || f.ICU != "78.3" || f.Locale != "en-US" {
		t.Fatal("oracle environment changed")
	}
	if len(f.Cases) != 2097 || len(f.Parses) != 45 || len(f.Pairs) != 529 || len(f.Normalizations) != 40016 {
		t.Fatal("oracle inventory changed")
	}
	// This finite oracle is NOT a production collator, nor evidence of arbitrary
	// Unicode collation parity. Missing pairs fail rather than approximating.
	pairs := map[[2]string]int{}
	for _, p := range f.Pairs {
		pairs[[2]string{admissionString(t, p.A), admissionString(t, p.B)}] = p.Result
	}
	compare := func(a, b string) (int, error) {
		v, ok := pairs[[2]string{a, b}]
		if !ok {
			return 0, fmt.Errorf("missing test comparator pair %q %q", a, b)
		}
		return v, nil
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			input, topics, config := admissionDecode(t, c.Raw)
			config.LocaleCompare = compare
			got, err := AssignTopic(input, topics, config)
			if c.Error != "" {
				if err == nil || err.Error() != c.Error {
					t.Fatalf("got %v want %s", err, c.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := admissionExpected(t, c.Result)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v want %#v", got, want)
			}
		})
	}
	for n, p := range f.Normalizations {
		text := admissionString(t, p.Raw)
		units, err := jsjson.UTF16(text)
		if err != nil {
			t.Fatal(err)
		}
		got := normalizeCueUnits(units)
		want := admissionString(t, p.Result)
		if got != want {
			t.Fatalf("NFC probe %d: got %q want %q for %s", n, got, want, p.Raw)
		}
	}
	for n, p := range f.Parses {
		t.Run(fmt.Sprintf("parse-%d", n), func(t *testing.T) {
			got, err := ParseTopicCue(admissionString(t, p.Raw))
			if err != nil {
				t.Fatal(err)
			}
			var want *string
			if p.Result != nil {
				s := admissionString(t, *p.Result)
				want = &s
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v for %s", got, want, p.Raw)
			}
		})
	}
}
func TestAdmissionComparatorBlocker(t *testing.T) {
	cue := "new"
	slots := 2.0
	input := TopicInput{SceneID: "s", Now: 2, Cue: &cue}
	topics := []TopicSnapshot{{ID: "a", SceneID: "s", TouchedAt: 1, SendState: "none"}, {ID: "A", SceneID: "s", TouchedAt: 1, SendState: "none"}}
	if _, err := AssignTopic(input, topics, AdmissionConfig{MaxSlots: &slots}); err != ErrAdmissionCollation {
		t.Fatalf("must not approximate collation: %v", err)
	}
	if _, err := AssignTopic(input, topics, AdmissionConfig{}); err != nil {
		t.Fatal("comparison cannot affect under-capacity result", err)
	}
	topics[1].TouchedAt = 0
	got, err := AssignTopic(input, topics, AdmissionConfig{MaxSlots: &slots})
	if err != nil || got.EvictTopicID == nil || *got.EvictTopicID != "A" {
		t.Fatalf("timestamp ordering requires no collation: %#v %v", got, err)
	}
	before := append([]TopicSnapshot(nil), topics...)
	_, _ = AssignTopic(input, topics, AdmissionConfig{MaxSlots: &slots})
	if !reflect.DeepEqual(before, topics) {
		t.Fatal("mutated input")
	}
}
