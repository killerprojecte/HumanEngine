package learningbatch

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"strings"
)

//go:embed schemas.json prompts.json stage-schemas.json
var assets embed.FS

type schemas struct {
	raw      map[string]map[string]any
	compiled map[string]*jsonschema.Schema
	prompts  map[string]string
}

func loadSchemas() (*schemas, error) {
	s := &schemas{compiled: map[string]*jsonschema.Schema{}}
	b, _ := assets.ReadFile("schemas.json")
	if e := json.Unmarshal(b, &s.raw); e != nil {
		return nil, e
	}
	b, _ = assets.ReadFile("stage-schemas.json")
	var stages map[string]map[string]any
	if e := json.Unmarshal(b, &stages); e != nil {
		return nil, e
	}
	for k, v := range stages {
		s.raw[k] = v
	}
	b, _ = assets.ReadFile("prompts.json")
	if e := json.Unmarshal(b, &s.prompts); e != nil {
		return nil, e
	}
	s.raw["offsetInstant"] = s.raw["candidate0"]["properties"].(map[string]any)["validFrom"].(map[string]any)
	for name, raw := range s.raw {
		c := jsonschema.NewCompiler()
		if e := c.AddResource("https://learning.invalid/schema", withoutLengths(raw)); e != nil {
			return nil, e
		}
		v, e := c.Compile("https://learning.invalid/schema")
		if e != nil {
			return nil, e
		}
		s.compiled[name] = v
	}
	return s, nil
}
func native(v any) any {
	switch x := v.(type) {
	case *Object:
		m := map[string]any{}
		if x != nil {
			for _, p := range x.Members() {
				m[p.Key] = native(p.Value)
			}
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = native(v)
		}
		return a
	default:
		return v
	}
}
func (s *schemas) parse(v *Object, name string) (*Object, error) {
	if v == nil {
		return nil, errors.New("invalid_candidate")
	}
	v = v.Clone()
	if strings.HasPrefix(name, "candidate") {
		if _, ok := v.Get("importance"); !ok {
			_ = v.Set("importance", "medium")
		}
		if _, ok := v.Get("confidence"); !ok {
			_ = v.Set("confidence", .5)
		}
	}
	if e := s.compiled[name].Validate(native(v)); e != nil {
		return nil, e
	}
	if e := validateLengths(s.raw[name], native(v)); e != nil {
		return nil, e
	}
	if strings.HasPrefix(name, "candidate") {
		if trim(str(get(v, "content"))) == "" {
			return nil, errors.New("invalid_candidate")
		}
		knowledge := arr(get(v, "knowledge"))
		if len(knowledge) > 0 && get(v, "publicTechnicalDomain") != nil {
			return nil, errors.New("invalid_candidate")
		}
		for _, k := range knowledge {
			if get(get(k, "subject"), "kind") == "existing" || get(get(k, "object"), "kind") == "existing" {
				return nil, errors.New("invalid_candidate")
			}
			for _, span := range arr(get(k, "sourceSpans")) {
				if !contains(arr(get(v, "sourceMessageIds")), get(span, "messageId")) {
					return nil, errors.New("invalid_candidate")
				}
				if get(span, "stance") == "quoted" || get(span, "stance") == "reported" {
					if get(v, "kind") != "claim" || !contains([]any{"reported", "inferred", "hypothesis"}, get(v, "epistemicKind")) {
						return nil, errors.New("invalid_candidate")
					}
				}
			}
		}
		for _, key := range []string{"occurredAt", "validUntil"} {
			if t, ok := get(v, key).(string); ok && t != "" && !jsdate.FiniteDateParse(t) {
				return nil, errors.New("invalid_candidate")
			}
			if get(v, "subject") != nil && get(v, key) != nil {
				t := str(get(v, key))
				if s.compiled["offsetInstant"].Validate(t) != nil {
					return nil, errors.New("invalid_candidate")
				}
			}
		}
	}
	return v, nil
}
func candidateName(s Settings) string { return fmt.Sprintf("candidate%d", mode(s)) }
func sliceUTF16(s string, n int) string {
	u, e := jsjson.UTF16(s)
	if e != nil {
		return s
	}
	if len(u) > n {
		u = u[:n]
	}
	return jsjson.FromUTF16(u)
}

// The pinned source Zod measures strings in Unicode code points. Validate
// through jsjson as well so lone surrogate strings remain lossless.
func withoutLengths(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			if k != "minLength" && k != "maxLength" {
				out[k] = withoutLengths(v)
			}
		}
		return out
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = withoutLengths(v)
		}
		return a
	default:
		return v
	}
}
func validateLengths(schema map[string]any, v any) error {
	for _, kind := range []string{"oneOf", "anyOf"} {
		if branches, ok := schema[kind].([]any); ok {
			for _, branch := range branches {
				s := branch.(map[string]any)
				c := jsonschema.NewCompiler()
				if e := c.AddResource("https://learning.invalid/branch", withoutLengths(s)); e != nil {
					return e
				}
				compiled, e := c.Compile("https://learning.invalid/branch")
				if e != nil {
					return e
				}
				if compiled.Validate(v) == nil && validateLengths(s, v) == nil {
					return nil
				}
			}
			return errors.New("invalid_candidate")
		}
	}
	switch x := v.(type) {
	case string:
		n, e := jsjson.CodePointCount(x)
		if e != nil {
			return e
		}
		if min, ok := schema["minLength"].(float64); ok && float64(n) < min {
			return errors.New("invalid_candidate")
		}
		if max, ok := schema["maxLength"].(float64); ok && float64(n) > max {
			return errors.New("invalid_candidate")
		}
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		for k, v := range x {
			if sub, ok := props[k].(map[string]any); ok {
				if e := validateLengths(sub, v); e != nil {
					return e
				}
			}
		}
	case []any:
		if sub, ok := schema["items"].(map[string]any); ok {
			for _, v := range x {
				if e := validateLengths(sub, v); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

// ECMAScript whitespace differs from unicode.IsSpace (notably FEFF and 0085).
func trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r == 0xFEFF || r == 0xA0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200A) || r == 0x2028 || r == 0x2029 || r == 0x202F || r == 0x205F || r == 0x3000 || r == ' ' || (r >= 9 && r <= 13)
	})
}
