package replyplan

import (
	"embed"
	"fmt"
	"math"
	"regexp"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// These are generated from the actual pinned Zod source with io:"input". The
// native validator below preserves pinned Zod code-point lengths, safe integers, defaults,
// strict objects, and strip-unknown objects rather than substituting a generic
// JSON Schema implementation with different string-length semantics.
//
//go:embed schema/*.json
var schemaFiles embed.FS
var planSchemas [2]*Object
var semanticSchema *Object
var schemaPatterns = map[string]*regexp.Regexp{}

func init() {
	for i, name := range []string{"plan.json", "plan-suggestions.json", "semantic-state.json"} {
		data, err := schemaFiles.ReadFile("schema/" + name)
		if err != nil {
			panic(err)
		}
		v, err := jsjson.ParseBytes(data)
		if err != nil {
			panic(err)
		}
		s := object(v)
		if s == nil {
			panic("replyplan: invalid generated schema")
		}
		checkSchema(s)
		if i < 2 {
			planSchemas[i] = s
		} else {
			semanticSchema = s
		}
	}
}
func checkSchema(s *Object) {
	for _, m := range s.Members() {
		switch m.Key {
		case "$schema", "description", "title", "type", "const", "enum", "required", "default", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "minLength", "maxLength", "minItems", "maxItems", "format":
		case "pattern":
			r, err := regexp.Compile(str(m.Value))
			if err != nil {
				panic(err)
			}
			schemaPatterns[str(m.Value)] = r
		case "properties":
			for _, p := range object(m.Value).Members() {
				checkSchema(object(p.Value))
			}
		case "items", "additionalProperties", "propertyNames":
			if x := object(m.Value); x != nil {
				checkSchema(x)
			}
		case "anyOf", "allOf", "oneOf":
			for _, x := range array(m.Value) {
				checkSchema(object(x))
			}
		default:
			panic(fmt.Sprintf("replyplan: unsupported generated schema keyword %q", m.Key))
		}
	}
}
func parseSchema(schema *Object, value any) (any, bool) {
	if alternatives := array(get(schema, "anyOf")); alternatives != nil {
		for _, s := range alternatives {
			if v, ok := parseSchema(object(s), value); ok {
				return v, true
			}
		}
		return nil, false
	}
	if alternatives := array(get(schema, "oneOf")); alternatives != nil {
		var result any
		count := 0
		for _, s := range alternatives {
			if v, ok := parseSchema(object(s), value); ok {
				result = v
				count++
			}
		}
		return result, count == 1
	}
	if alternatives := array(get(schema, "allOf")); alternatives != nil {
		for _, s := range alternatives {
			var ok bool
			value, ok = parseSchema(object(s), value)
			if !ok {
				return nil, false
			}
		}
	}
	if has(schema, "const") && !deepEqual(get(schema, "const"), value) {
		return nil, false
	}
	if has(schema, "enum") && !some(get(schema, "enum"), func(v any) bool { return deepEqual(v, value) }) {
		return nil, false
	}
	typ := str(get(schema, "type"))
	if types, ok := get(schema, "type").([]any); ok {
		for _, t := range types {
			copy := schema.Clone()
			set(copy, "type", t)
			if v, valid := parseSchema(copy, value); valid {
				return v, true
			}
		}
		return nil, false
	}
	switch typ {
	case "string":
		s, ok := value.(string)
		if !ok {
			return nil, false
		}
		n, err := jsjson.CodePointCount(s)
		if err != nil {
			return nil, false
		}
		if !withinBounds(schema, "minLength", "maxLength", float64(n)) {
			return nil, false
		}
		if p, ok := get(schema, "pattern").(string); ok {
			r := schemaPatterns[p]
			// The source patterns are not multiline: JavaScript $ requires
			// the actual end of input, including after any line terminator.
			if !r.MatchString(s) {
				return nil, false
			}
		}
	case "number", "integer":
		n, ok := value.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, false
		}
		if typ == "integer" && !safeInteger(n) {
			return nil, false
		}
		if !withinBounds(schema, "minimum", "maximum", n) {
			return nil, false
		}
		if b, ok := get(schema, "exclusiveMinimum").(float64); ok && n <= b {
			return nil, false
		}
		if b, ok := get(schema, "exclusiveMaximum").(float64); ok && n >= b {
			return nil, false
		}
	case "boolean":
		if !isBool(value) {
			return nil, false
		}
	case "null":
		if value != nil {
			return nil, false
		}
	case "array":
		a, ok := value.([]any)
		if !ok || !withinBounds(schema, "minItems", "maxItems", float64(len(a))) {
			return nil, false
		}
		out := make([]any, len(a))
		for i, x := range a {
			if child := object(get(schema, "items")); child != nil {
				var valid bool
				out[i], valid = parseSchema(child, x)
				if !valid {
					return nil, false
				}
			} else {
				out[i] = clone(x)
			}
		}
		return out, true
	case "object":
		o := object(value)
		if o == nil {
			return nil, false
		}
		for _, r := range memberStrings(get(schema, "required")) {
			if !defined(o, r) {
				return nil, false
			}
		}
		properties := object(get(schema, "properties"))
		out := obj()
		if properties != nil {
			for _, p := range properties.Members() {
				child := object(p.Value)
				x, present := o.Get(p.Key)
				if !present || isUndefined(x) {
					if has(child, "default") {
						set(out, p.Key, clone(get(child, "default")))
					} else if present {
						set(out, p.Key, Undefined)
					}
					continue
				}
				parsed, valid := parseSchema(child, x)
				if !valid {
					return nil, false
				}
				set(out, p.Key, parsed)
			}
		}
		for _, p := range o.Members() {
			if properties != nil && has(properties, p.Key) {
				continue
			}
			additional := get(schema, "additionalProperties")
			if additional == false {
				return nil, false
			}
			if child := object(additional); child != nil {
				if keySchema := object(get(schema, "propertyNames")); keySchema != nil {
					if _, valid := parseSchema(keySchema, p.Key); !valid {
						return nil, false
					}
				}
				parsed, valid := parseSchema(child, p.Value)
				if !valid {
					return nil, false
				}
				if p.Key != "__proto__" {
					set(out, p.Key, parsed)
				}
			}
		}
		return out, true
	case "":
		// Zod unknown retains the original value (record containers still parse).
		return value, true
	default:
		panic("replyplan: unsupported generated schema type " + typ)
	}
	return clone(value), true
}
func withinBounds(s *Object, min, max string, n float64) bool {
	if v, ok := get(s, min).(float64); ok && n < v {
		return false
	}
	if v, ok := get(s, max).(float64); ok && n > v {
		return false
	}
	return true
}
func parsePlan(value any, suggestions bool) (*Object, error) {
	i := 0
	if suggestions {
		i = 1
	}
	v, ok := parseSchema(planSchemas[i], value)
	if !ok {
		return nil, errCode("planner_invalid_plan")
	}
	return object(v), nil
}

var finalTools [2]*Object

func init() {
	for i, name := range []string{"plan-tool.json", "plan-tool-suggestions.json"} {
		data, err := schemaFiles.ReadFile("schema/" + name)
		if err != nil {
			panic(err)
		}
		value, err := jsjson.ParseBytes(data)
		if err != nil {
			panic(err)
		}
		finalTools[i] = object(value)
	}
}

// PlanTool returns the exact source-generated public tool contract, including
// defaults, descriptions and suggestions gating. Each caller owns its copy.
func PlanTool(suggestions bool) *Object {
	i := 0
	if suggestions {
		i = 1
	}
	return object(clone(finalTools[i]))
}
