package topicroute

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func stringsOf(value any) []string {
	values := value.([]any)
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = v.(string)
	}
	return out
}
func field(o *jsjson.Object, key string) any { v, _ := o.Get(key); return v }
func decodeInput(t *testing.T, raw string) Reassignment {
	t.Helper()
	v, err := jsjson.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*jsjson.Object)
	out := Reassignment{DeliveredIDs: stringsOf(field(o, "deliveredIds")), CurrentTopicID: field(o, "currentTopicId").(string)}
	if v, ok := o.Get("selectedTopicId"); ok {
		s := v.(string)
		out.SelectedTopicID = &s
	}
	if v, ok := o.Get("maxSlots"); ok {
		n := v.(float64)
		out.MaxSlots = &n
	}
	for _, v := range field(o, "groups").([]any) {
		g := v.(*jsjson.Object)
		out.Groups = append(out.Groups, Group{field(g, "topicId").(string), stringsOf(field(g, "messageIds"))})
	}
	for _, v := range field(o, "retained").([]any) {
		r := v.(*jsjson.Object)
		out.Retained = append(out.Retained, Retained{field(r, "id").(string), stringsOf(field(r, "messageIds")), field(r, "protected").(bool)})
	}
	return out
}
func TestReassignmentActualNodeOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/reassignment-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Source, SourceHash string
		Cases              []struct {
			Name, Raw string
			Result    bool
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("../../..", fixture.Source))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(source)) != fixture.SourceHash {
		t.Fatal("source changed: regenerate and review oracle")
	}
	if len(fixture.Cases) != 908 {
		t.Fatal("oracle inventory changed")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			input := decodeInput(t, c.Raw)
			if got := ValidReassignment(input); got != c.Result {
				t.Fatalf("got %v want %v: %s", got, c.Result, c.Raw)
			}
		})
	}
}
func TestReassignmentInputNotMutated(t *testing.T) {
	selected := "proposal"
	input := Reassignment{Groups: []Group{{"proposal", []string{"a"}}}, DeliveredIDs: []string{"a"}, CurrentTopicID: "current", SelectedTopicID: &selected, Retained: []Retained{{"current", []string{"a"}, true}}}
	before, _ := json.Marshal(input)
	if !ValidReassignment(input) {
		t.Fatal("protected running topic lost its selected identity")
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("validation mutated routing state")
	}
}
func TestReassignmentNumericBoundary(t *testing.T) {
	for _, test := range []struct {
		Max  float64
		Want bool
	}{{math.NaN(), false}, {math.Inf(1), true}, {math.Inf(-1), false}, {-1, false}, {0, true}} {
		if got := ValidReassignment(Reassignment{MaxSlots: &test.Max}); got != test.Want {
			t.Fatalf("max %v: got %v", test.Max, got)
		}
	}
}
