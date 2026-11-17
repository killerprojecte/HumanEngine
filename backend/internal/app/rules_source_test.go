package app

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func TestRulesPublishedTypeScriptDifferential(t *testing.T) {
	raw, err := os.ReadFile("testdata/rules-p1-source.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsjson.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	fixture := parsed.(*jsjson.Object)
	if rulesString(fixture, "description") != rulesToolDescription {
		t.Fatal("tool guidance differs from published TypeScript")
	}
	get := func(o *jsjson.Object, key string) any { v, _ := o.Get(key); return v }
	records := get(fixture, "records").([]any)
	for i, v := range get(fixture, "cases").([]any) {
		row := v.(*jsjson.Object)
		q, e := parseRulesQuery(get(row, "input"))
		success := get(row, "success") == true
		if (e == nil) != success {
			t.Fatalf("query %d success=%v want=%v: %v", i, e == nil, success, e)
		}
		if !success {
			continue
		}
		ids := []any{}
		for _, r := range records {
			record := r.(*jsjson.Object)
			match, e := q.matches(record)
			if e != nil {
				t.Fatal(e)
			}
			if match && habitservice.InScope(habitservice.Context{SceneID: "s"}, record) {
				ids = append(ids, rulesString(record, "id"))
			}
		}
		actual, _ := jsjson.Stringify(ids)
		expected, _ := jsjson.Stringify(get(row, "ids"))
		if actual != expected {
			t.Fatalf("query %d ids %s want %s", i, actual, expected)
		}
	}
	for _, v := range get(fixture, "budgets").([]any) {
		row := v.(*jsjson.Object)
		t.Run(rulesString(row, "name"), func(t *testing.T) {
			limit := get(row, "maxBytes").(float64)
			b, e := replyplan.NewEvidenceBudget(&limit)
			if e != nil {
				t.Fatal(e)
			}
			messages := []*jsjson.Object{memoryToolObject("role", "user", "content", strings.Repeat("c", int(get(row, "padding").(float64))))}
			value := memoryToolObject("manual", []any{}, "learned", []any{memoryToolObject("content", strings.Repeat("x", int(get(row, "content").(float64))))})
			result, e := b.Receipt(messages, memoryToolObject("id", "rules", "toolName", "rules.list", "value", value), "call", nil)
			if code := rulesString(row, "error"); code != "" {
				if e == nil || e.Error() != code {
					t.Fatalf("error %v want %s", e, code)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			actual, _ := jsjson.Stringify(get(result.Receipt, "value"))
			expected, _ := jsjson.Stringify(get(row, "expected"))
			if actual != expected {
				t.Fatalf("receipt %s want %s", actual, expected)
			}
		})
	}
	if source := os.Getenv("HE_P1_TS_SOURCE"); source != "" {
		for _, v := range get(fixture, "sources").([]any) {
			row := v.(*jsjson.Object)
			data, e := os.ReadFile(filepath.Join(source, rulesString(row, "path")))
			if e != nil {
				t.Fatal(e)
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != rulesString(row, "sha256") {
				t.Fatalf("published TS source changed: %s", rulesString(row, "path"))
			}
		}
	}
}
