package experienceview

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"path/filepath"
	"testing"
)

func TestActualSourceExperienceProjectionAndVerify(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	fixture, e := jsjson.Parse(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	for _, source := range get(fixture, "sources").(*Object).Members() {
		body, e := os.ReadFile(filepath.Join("../../..", source.Key))
		if e != nil {
			t.Fatal(e)
		}
		if fmt.Sprintf("%x", sha256.Sum256(body)) != source.Value {
			t.Fatalf("stale source fixture %s", source.Key)
		}
	}
	stringify := func(v any) string {
		s, e := jsjson.Stringify(v)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	for _, test := range get(fixture, "projections").([]any) {
		t.Run(get(test, "name").(string), func(t *testing.T) {
			input, _ := get(test, "input").(*Object)
			before := stringify(input)
			out, e := Project(input)
			if want := get(test, "error"); want != nil {
				if e == nil || e.Error() != want {
					t.Fatalf("error got %v want %v", e, want)
				}
			} else if e != nil || stringify(out) != stringify(get(test, "value")) {
				t.Fatalf("got %s / %v want %s", stringify(out), e, stringify(get(test, "value")))
			}
			if stringify(input) != before {
				t.Fatal("input mutated")
			}
		})
	}
	for _, test := range get(fixture, "verifications").([]any) {
		t.Run("verify/"+get(test, "name").(string), func(t *testing.T) {
			before := stringify(test)
			calls := []any{}
			scene, _ := get(test, "sceneId").(string)
			page, _ := get(test, "page").(*Object)
			valid := Verify(scene, page, func(scene string, refs *Object) (*Object, error) {
				calls = append(calls, []any{scene, clone(refs)})
				if truthy(get(test, "fail")) {
					return nil, errors.New("synthetic callback failure")
				}
				current, _ := get(test, "current").(*Object)
				return current, nil
			})
			if valid != get(test, "valid") || stringify(calls) != stringify(get(test, "calls")) {
				t.Fatalf("got %v calls %s want %v calls %s", valid, stringify(calls), get(test, "valid"), stringify(get(test, "calls")))
			}
			if stringify(test) != before {
				t.Fatal("verify mutated input")
			}
		})
	}
}
