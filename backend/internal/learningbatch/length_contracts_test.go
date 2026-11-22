package learningbatch

import (
	"strconv"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCompiledLearningLengthTreeMirrorsEmbeddedSchemas(t *testing.T) {
	s, err := loadSchemas()
	if err != nil {
		t.Fatal(err)
	}
	var walk func(string, map[string]any, *jsonschema.Schema)
	walk = func(path string, raw map[string]any, compiled *jsonschema.Schema) {
		if compiled == nil || compiled.DraftVersion != 2020 || compiled.MinLength != nil || compiled.MaxLength != nil {
			t.Fatalf("%s: expected length-stripped compiled Draft 2020-12 node", path)
		}
		for _, keyword := range []string{"$id", "$ref", "$dynamicRef", "$recursiveRef", "$anchor", "$dynamicAnchor", "$recursiveAnchor", "$vocabulary", "unevaluatedProperties", "unevaluatedItems"} {
			if _, ok := raw[keyword]; ok {
				t.Fatalf("%s: recheck standalone branch equivalence for %s", path, keyword)
			}
		}
		for _, union := range []struct {
			kind     string
			children []*jsonschema.Schema
		}{
			{"oneOf", compiled.OneOf},
			{"anyOf", compiled.AnyOf},
		} {
			rawChildren, _ := raw[union.kind].([]any)
			if len(rawChildren) != len(union.children) {
				t.Fatalf("%s: compiled %s child count differs", path, union.kind)
			}
			for i, rawChild := range rawChildren {
				child := union.children[i]
				branchPath := "/" + union.kind + "/" + strconv.Itoa(i)
				if child == nil || !strings.HasSuffix(child.Location, branchPath) {
					t.Fatalf("%s%s: compiled branch order differs", path, branchPath)
				}
				walk(path+branchPath, rawChild.(map[string]any), child)
			}
		}
		props, _ := raw["properties"].(map[string]any)
		if len(props) != len(compiled.Properties) {
			t.Fatalf("%s: compiled properties differ", path)
		}
		for key, child := range props {
			walk(path+"/properties/"+key, child.(map[string]any), compiled.Properties[key])
		}
		items, ok := raw["items"].(map[string]any)
		if _, exists := raw["items"]; exists != ok || ok != (compiled.Items2020 != nil) {
			t.Fatalf("%s: expected matching object items", path)
		}
		if ok {
			walk(path+"/items", items, compiled.Items2020)
		}
	}
	for name, raw := range s.raw {
		walk(name, raw, s.compiled[name])
	}
}

func TestLengthUnionPreservesIndependentShapeAndCodePointChecks(t *testing.T) {
	// Both anyOf children accept the same length-stripped string shape. The
	// date-time annotation is not an assertion in the embedded draft.
	str := func(n float64) map[string]any {
		return map[string]any{"type": "string", "minLength": n, "maxLength": n, "format": "date-time"}
	}
	for _, tc := range []struct {
		name string
		raw  map[string]any
	}{
		{"anyOf tries next matching shape", map[string]any{"anyOf": []any{str(4), str(2)}}},
		{"oneOf takes precedence in length pass", map[string]any{"oneOf": []any{str(2), map[string]any{"type": "number"}}, "anyOf": []any{str(4)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.raw["$schema"] = "https://json-schema.org/draft/2020-12/schema"
			c := jsonschema.NewCompiler()
			const url = "https://learning.invalid/test-length-union"
			if err := c.AddResource(url, withoutLengths(tc.raw)); err != nil {
				t.Fatal(err)
			}
			compiled, err := c.Compile(url)
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []struct {
				json string
				want bool
			}{
				{`"\ud83d\ude00\ud800"`, true}, // one pair, one lone surrogate
				{`"\ud800"`, false},
				{`"\ud83d\ude00\ud800x"`, false},
				{`false`, false},
			} {
				value, err := jsjson.ParseBytes([]byte(input.json))
				if err != nil {
					t.Fatal(err)
				}
				if got := validateLengths(tc.raw, compiled, value) == nil; got != input.want {
					t.Errorf("%s: valid=%v want %v", input.json, got, input.want)
				}
			}
		})
	}
}
