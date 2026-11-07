package botruntime

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type groundingFixture struct {
	Node, Unicode string
	Sources       []struct{ Path, SHA256 string }
	HanRanges     [][2]rune
	Fallbacks     []struct{ Input, Expected string }
	Cases         []struct{ Name, Input, Expected string }
}

func loadGroundingFixture(t *testing.T) groundingFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/grounding.json")
	if err != nil {
		t.Fatal(err)
	}
	var f groundingFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Node != "v24.22.1" || f.Unicode != "17.0" {
		t.Fatalf("unexpected oracle runtime: %s Unicode %s", f.Node, f.Unicode)
	}
	return f
}
func TestGroundingSourceHashes(t *testing.T) {
	for _, source := range loadGroundingFixture(t).Sources {
		raw, err := os.ReadFile(filepath.Join("../../..", source.Path))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != source.SHA256 {
			t.Fatalf("source drift: %s", source.Path)
		}
	}
}
func groundTestParse(t *testing.T, text string) any {
	t.Helper()
	v, err := jsjson.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func groundTestObject(fields ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(fields); i += 2 {
		if err := o.Set(fields[i].(string), fields[i+1]); err != nil {
			panic(err)
		}
	}
	return o
}
func groundTestSegments(v any) []dispatch.Segment {
	items := v.([]any)
	segments := make([]dispatch.Segment, len(items))
	for i, item := range items {
		parts := groundGet(item, "parts").([]any)
		segments[i].Parts = make([]json.RawMessage, len(parts))
		for j, part := range parts {
			segments[i].Parts[j] = groundJSON(part)
		}
		if id, ok := groundGet(item, "replyToId").(string); ok {
			segments[i].ReplyToID = &id
		}
	}
	return segments
}
func groundTestSegmentValue(segments []dispatch.Segment) []any {
	out := make([]any, len(segments))
	for i, segment := range segments {
		parts := make([]any, len(segment.Parts))
		for j, part := range segment.Parts {
			parts[j] = groundParse(part)
		}
		o := groundTestObject("parts", parts)
		if segment.ReplyToID != nil {
			_ = o.Set("replyToId", *segment.ReplyToID)
		}
		out[i] = o
	}
	return out
}
func groundTestCode(code string) any {
	if code == "" {
		return nil
	}
	return code
}
func groundTestTrace(t *testing.T, input string) any {
	t.Helper()
	g := NewGrounding()
	out := []any{}
	for _, step := range groundTestParse(t, input).([]any) {
		switch groundGet(step, "op") {
		case "source":
			parts := groundGet(step, "parts").([]any)
			raw := make([]json.RawMessage, len(parts))
			for i, part := range parts {
				raw[i] = groundJSON(part)
			}
			g.AddFeedbackSource(groundString(groundGet(step, "id")), raw)
		case "evidence":
			g.AddEvidence(groundGet(step, "value"))
		case "tool":
			g.ObserveTool(groundString(groundGet(step, "name")), groundGet(step, "value"))
		case "history":
			g.ObserveCoreHistory(groundGet(step, "value"), groundString(groundGet(step, "sceneId")), groundString(groundGet(step, "mode")))
		case "probe":
			segments := groundTestSegments(groundGet(step, "segments"))
			normalized := g.NormalizeFeedback(segments)
			targets := []any{}
			for _, id := range groundGet(step, "ids").([]any) {
				targets = append(targets, g.IsFeedbackTarget(id.(string)))
			}
			out = append(out, groundTestObject(
				"has", g.HasFeedbackReceipts(), "targets", targets,
				"check", groundTestCode(g.Check(segments)), "normalized", groundTestSegmentValue(normalized),
				"normalizedCheck", groundTestCode(g.Check(normalized)), "original", groundTestSegmentValue(segments),
			))
		default:
			t.Fatalf("unknown oracle operation %v", groundGet(step, "op"))
		}
	}
	return out
}

// Compare decoded jsjson values without erasing lone surrogates or treating
// JSON object property order as a semantic difference in send segment structs.
func groundTestEqual(a, b any) bool {
	switch x := a.(type) {
	case *jsjson.Object:
		y, ok := b.(*jsjson.Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, m := range x.Members() {
			other, ok := y.Get(m.Key)
			if !ok || !groundTestEqual(m.Value, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, item := range x {
			if !groundTestEqual(item, y[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}
func TestGroundingActualSourceOracle(t *testing.T) {
	fixture := loadGroundingFixture(t)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := groundTestTrace(t, tc.Input)
			want := groundTestParse(t, tc.Expected)
			if !groundTestEqual(got, want) {
				actual, _ := jsjson.Stringify(got)
				t.Fatalf("source differential mismatch\nactual: %s\nsource: %s", actual, tc.Expected)
			}
		})
	}
}
func TestGroundingSourceFallbacksAndUnicode(t *testing.T) {
	f := loadGroundingFixture(t)
	if !reflect.DeepEqual(groundHanRanges, f.HanRanges) {
		t.Fatal("generated Han ranges differ from exhaustive actual-source observations")
	}
	for _, tc := range f.Fallbacks {
		input := groundTestParse(t, tc.Input).(string)
		if got := GroundingFallback(input); got != tc.Expected {
			t.Errorf("fallback for %s: %s, want %s", tc.Input, got, tc.Expected)
		}
	}
	// Exercise every generated boundary, including supplementary-plane Han and
	// lone-surrogate/non-Han neighbors, through the actual production selector.
	for _, r := range f.HanRanges {
		for _, cp := range []rune{r[0] - 1, r[0], r[1], r[1] + 1} {
			want := false
			for _, candidate := range f.HanRanges {
				if cp >= candidate[0] && cp <= candidate[1] {
					want = true
					break
				}
			}
			if got := groundHasHan(string(cp)); got != want {
				t.Errorf("Han U+%X = %v, want %v", cp, got, want)
			}
		}
	}
}

// Builder-backed accumulation preserves exact source evidence while avoiding a
// full copy of everything already retained on every newly visited string.
func TestGroundingEvidenceAppendPreservesEarlierViews(t *testing.T) {
	g := NewGrounding()
	g.AddEvidence("first evidence")
	earlier := g.evidence.String()
	for i := 0; i < 10000; i++ {
		g.AddEvidence("later evidence")
	}
	if earlier != "\nfirst evidence" {
		t.Fatal("published string changed", earlier)
	}
	if got := g.evidence.Len(); got != len("\nfirst evidence")+10000*len("\nlater evidence") {
		t.Fatal("lost or truncated evidence", got)
	}
}
