package contract

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestStringsMatchNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/http-strings-node.json")
	if err != nil {
		t.Fatal(err)
	}
	// Each input/output is nested JSON *text*, so this outer scalar-UTF8
	// decoder never sees the represented surrogate escapes as code units.
	var corpus struct {
		Node     string
		Fixtures []struct {
			Schema, Label, Input, Output string
			Valid                        bool
		}
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Node != "v24.22.1" {
		t.Fatalf("unexpected oracle %q", corpus.Node)
	}
	schemas, err := NewSchemas()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range corpus.Fixtures {
		t.Run(f.Schema+"/"+f.Label, func(t *testing.T) {
			actual, err := schemas.Parse(f.Schema, []byte(f.Input))
			if (err == nil) != f.Valid {
				t.Fatalf("accepted=%t want=%t: %v", err == nil, f.Valid, err)
			}
			if !f.Valid {
				return
			}
			want, err := jsjson.Parse(f.Output)
			if err != nil {
				t.Fatal(err)
			}
			want, err = validationValue(want)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatal("parsed values differ from Node (including original UTF-16 identity)")
			}
		})
	}
}

// This is an explicit audit of the reachable schema vocabulary. Extending the
// generated contract to a new applicator requires extending the length walker,
// not silently dropping lengths in a schema branch the walker cannot reach.
// No shadow strings are used: enum, regex, required/property identity, and the
// ModelConfig duplicate/reference refinements all see the exact original bytes.
func TestStringValidationContract(t *testing.T) {
	schemas, err := NewSchemas()
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, key := range []string{"type", "properties", "items", "$ref", "required", "additionalProperties", "default", "enum", "minLength", "maxLength", "minItems", "maxItems", "minimum", "maximum", "pattern", "format"} {
		allowed[key] = true
	}
	patterns := map[string]bool{
		`^[a-zA-Z0-9_.:-]{1,160}$`:                                 true,
		`^(?:openai-chat|openai-responses|[a-z0-9-]+:[a-z0-9-]+)$`: true,
		`^(?:[A-Z][A-Z0-9_]*)?$`:                                   true,
	}
	var visit func(map[string]any)
	visit = func(schema map[string]any) {
		for key := range schema {
			if !allowed[key] {
				t.Fatalf("audit new schema keyword %q for lossless validation", key)
			}
		}
		if _, ok := schema["$ref"]; ok {
			visit(schemas.resolve(schema))
			return
		}
		for _, key := range []string{"minLength", "maxLength"} {
			if _, ok := schema[key]; ok && schema["type"] != "string" {
				t.Fatal("length on unsupported type")
			}
		}
		if pattern, ok := schema["pattern"].(string); ok && !patterns[pattern] {
			t.Fatalf("audit non-ASCII/new regex %q", pattern)
		}
		if format, ok := schema["format"].(string); ok && format != "uri" {
			t.Fatalf("audit new format %q", format)
		}
		if properties, ok := schema["properties"].(map[string]any); ok {
			if schema["additionalProperties"] != false {
				t.Fatal("audit permissive object validation")
			}
			for _, value := range properties {
				visit(value.(map[string]any))
			}
		}
		if items, ok := schema["items"].(map[string]any); ok {
			visit(items)
		}
	}
	for _, name := range []string{"Settings", "ModelConfig", "Scene"} {
		visit(schemas.definition(name))
	}
}
