package replyplan

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type handoffOracle struct {
	sourceOracle
	Loops []fixtureCase `json:"loops"`
}

func loadHandoffOracle(t *testing.T) handoffOracle {
	t.Helper()
	data, err := os.ReadFile("testdata/handoff.json")
	if err != nil {
		t.Fatal(err)
	}
	var f handoffOracle
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Node != "v24.22.1" {
		t.Fatal(f.Node)
	}
	return f
}
func TestHandoffSourceHashes(t *testing.T) {
	for _, source := range loadHandoffOracle(t).Sources {
		data, err := os.ReadFile(filepath.Join("../../..", source.Path))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != source.SHA256 {
			t.Fatalf("source drift %s", source.Path)
		}
	}
}
func TestHandoffActualSourceOracle(t *testing.T) {
	runHandoffOracle(t, fixtureComparator(loadOracle(t)))
}
func runHandoffOracle(t *testing.T, compare LocaleCompare) {
	t.Helper()
	f := loadHandoffOracle(t)
	dates, err := jsdate.New(f.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(compare, dates)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range f.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			in := parse(t, tc.Input)
			var value any
			var err error
			switch tc.Kind {
			case "brief":
				value, err = p.BuildReplyBrief(object(get(in, "plan")), objectSlice(get(in, "evidence")), objectSlice(get(in, "actionReceipts")), array(get(in, "imageContext")), memberStrings(get(in, "relatedMessageIds")))
			case "knowledge":
				value = KnowledgeEvidence(get(in, "value"))
			case "select":
				value = fixtureSelect(t, in)
			case "pack":
				value = fixturePack(t, p, in)
			default:
				t.Fatal(tc.Kind)
			}
			checkHandoffJSON(t, encoded(t, captured(value, err)), tc.Expected)
		})
	}
	for _, tc := range f.Loops {
		t.Run("loop/"+tc.Name, func(t *testing.T) {
			in := parse(t, tc.Input)
			built := runBuild(t, p, in)
			result := get(built, "result")
			if has(result, "error") {
				t.Fatal(encoded(t, result))
			}
			value := get(result, "value")
			brief, err := p.BuildReplyBrief(object(get(value, "plan")), objectSlice(get(value, "evidence")), objectSlice(get(in, "actionReceipts")), array(get(in, "imageContext")), memberStrings(get(in, "relatedMessageIds")))
			if err != nil {
				t.Fatal(err)
			}
			upstream := []any{}
			for _, receipt := range array(get(value, "evidence")) {
				coverage := get(receipt, "coverage")
				if get(coverage, "reason") == "planner_evidence_budget" {
					upstream = append(upstream, obj(member("evidenceId", get(receipt, "id")), member("reason", "planner_evidence_budget"), member("omittedRecords", get(coverage, "omittedRecords"))))
				}
			}
			limit := float64(100000)
			if n, ok := get(in, "replyLimit").(float64); ok {
				limit = n
			}
			packInput := obj(member("brief", brief), member("envelope", parse(t, `{"now":"2026-11-01T00:00:00Z","timezone":"UTC","messages":[{"id":"m1","senderId":"speaker","parts":[{"type":"text","text":"Literal [image/unknown]."}]}]}`)), member("budget", obj(member("mode", "bytes"), member("limit", limit))), member("contextLayout", get(in, "contextLayout")), member("serializer", get(in, "serializer")), member("upstreamOmissions", upstream))
			packed := fixturePack(t, p, packInput)
			got := obj(member("events", get(built, "events")), member("result", result), member("brief", brief), member("packed", packed))
			checkHandoffJSON(t, encoded(t, got), tc.Expected)
		})
	}
}
func checkHandoffJSON(t *testing.T, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	start := i - 120
	if start < 0 {
		start = 0
	}
	ge, we := i+500, i+500
	if ge > len(got) {
		ge = len(got)
	}
	if we > len(want) {
		we = len(want)
	}
	t.Fatalf("JSON differs at byte %d (lengths %d/%d)\ngot: %s\nwant: %s", i, len(got), len(want), got[start:ge], want[start:we])
}
func fixtureCapabilities(t *testing.T, input any, trace *[]any) (func(ReadonlyJSON) (string, error), SelectionBudget) {
	t.Helper()
	serialCalls, tokenCalls := 0, 0
	var projection *ContextProjection
	if get(input, "serializer") == "projection" {
		projection = NewContextProjection("scene")
	}
	serialize := func(value ReadonlyJSON) (string, error) {
		serialCalls++
		serialized, err := value.Stringify()
		if err != nil {
			return "", err
		}
		*trace = append(*trace, obj(member("kind", "serialize"), member("value", serialized)))
		if get(input, "mutateOriginal") == true && serialCalls == 1 {
			if has(input, "candidates") {
				set(object(get(array(get(input, "candidates"))[0], "value")), "text", "CHANGED CALLER")
			} else {
				set(object(get(get(input, "brief"), "plan")), "strategy", "CHANGED CALLER")
				set(object(get(input, "envelope")), "now", "CHANGED CALLER")
			}
		}
		switch get(input, "serializer") {
		case "throw":
			return "", errCode("PRIVATE_ERROR")
		case "invalid":
			return "{", nil
		case "rawSurrogate":
			return "\"" + jsjson.FromUTF16([]uint16{0xd800}) + "\"", nil
		case "whitespaceJSON":
			return "  [null]  ", nil
		case "unstable":
			return stringify(obj(member("value", value.value), member("n", float64(serialCalls))))
		case "finalGrowth":
			pad := ""
			if serialCalls >= 3 {
				for i := 0; i < 50; i++ {
					pad += "x"
				}
			}
			return stringify(obj(member("pad", pad)))
		case "envelope":
			return stringify(obj(member("now", "fixture"), member("evidence", value.value), member("constraints", []any{"不可猜测"})))
		}
		if projection != nil {
			return value.ProjectedStringify(projection, []string{"now"})
		}
		return serialized, nil
	}
	budget := SelectionBudget{Mode: "bytes", Limit: 100000}
	if raw := get(input, "budget"); raw != nil {
		budget.Mode = str(get(raw, "mode"))
		if n, ok := get(raw, "limit").(float64); ok {
			budget.Limit = n
		} else {
			budget.Limit = math.NaN()
		}
	}
	if budget.Mode == "tokens" {
		token := get(input, "tokenizer")
		verified, version, identity := true, "fixture-v1", "fixture-model"
		if has(token, "verified") {
			verified = get(token, "verified") == true
		}
		if has(token, "version") {
			version = str(get(token, "version"))
		}
		if has(token, "modelIdentity") {
			identity = str(get(token, "modelIdentity"))
		}
		budget.Tokenizer = &VerifiedTokenizer{Verified: verified, Version: version, ModelIdentity: identity, Count: func(serialized string) (float64, error) {
			tokenCalls++
			*trace = append(*trace, obj(member("kind", "tokenize"), member("serialized", serialized)))
			switch get(token, "mode") {
			case "throw":
				return 0, errCode("PRIVATE_ERROR")
			case "sequence":
				return float64(tokenCalls), nil
			case "constant":
				n, ok := get(token, "value").(float64)
				if !ok {
					return math.NaN(), nil
				}
				return n, nil
			case "nan":
				return math.NaN(), nil
			case "infinity":
				return math.Inf(1), nil
			case "utf16":
				n, err := jsjson.UTF16Len(serialized)
				return float64(n), err
			default:
				text, err := jsjson.UTF8Text(serialized)
				return float64(len(text)), err
			}
		}}
	}
	return serialize, budget
}
func fixtureSelect(t *testing.T, input any) *Object {
	trace := []any{}
	serialize, budget := fixtureCapabilities(t, input, &trace)
	var selected []string
	if has(input, "selectedIds") {
		selected = memberStrings(get(input, "selectedIds"))
		if selected == nil {
			selected = []string{}
		}
	}
	result := SelectEvidenceGroups(SelectionInput{Candidates: get(input, "candidates"), SelectedIDs: selected, Serialize: serialize, Budget: budget})
	return obj(member("result", result.value), member("trace", trace))
}
func fixturePack(t *testing.T, p *Policy, input any) *Object {
	trace := []any{}
	serialize, budget := fixtureCapabilities(t, input, &trace)
	result := p.PackReplyEvidence(PackReplyEvidenceInput{Brief: object(get(input, "brief")), Envelope: object(get(input, "envelope")), Serialize: serialize, Budget: budget, ContextLayout: str(get(input, "contextLayout")), MandatoryEvidenceIDs: memberStrings(get(input, "mandatoryEvidenceIds")), UpstreamOmissions: objectSlice(get(input, "upstreamOmissions"))})
	return obj(member("result", result.value), member("trace", trace))
}
