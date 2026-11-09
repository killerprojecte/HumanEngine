package learningbatch

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// CandidateParser reuses the verified candidate schema, but contextual proposals
// may reference already-exposed knowledge entities. Source-only calls keep their
// existing stricter refinement. Instances are immutable after construction.
type CandidateParser struct {
	schemas *schemas
	ordered *Object
}

func NewCandidateParser() (*CandidateParser, error) {
	s, e := loadSchemas()
	if e != nil {
		return nil, e
	}
	raw, _ := assets.ReadFile("schemas.json")
	v, e := jsjson.ParseBytes(raw)
	if e != nil {
		return nil, e
	}
	return &CandidateParser{s, v.(*Object)}, nil
}
func (p *CandidateParser) ParseContextual(v *Object, s Settings) (*Object, error) {
	name := candidateName(s)
	v, e := p.schemas.parseCandidate(v, name, false)
	if e != nil {
		return nil, e
	}
	return orderCandidate(v, get(p.ordered, name)).(*Object), nil
}

// Zod reconstructs properties in schema order. Dedup must not depend on the
// caller's JSON property insertion order, including nested discriminated unions.
func orderCandidate(v, schema any) any {
	if alternatives := arr(get(schema, "oneOf")); len(alternatives) > 0 {
		for _, branch := range alternatives {
			if matchesBranch(v, branch) {
				return orderCandidate(v, branch)
			}
		}
	}
	if alternatives := arr(get(schema, "anyOf")); len(alternatives) > 0 {
		for _, branch := range alternatives {
			if matchesBranch(v, branch) {
				return orderCandidate(v, branch)
			}
		}
	}
	switch x := v.(type) {
	case *Object:
		props, _ := get(schema, "properties").(*Object)
		if props == nil {
			return x.Clone()
		}
		out := new(Object)
		for _, p := range props.Members() {
			if value, ok := x.Get(p.Key); ok {
				_ = out.Set(p.Key, orderCandidate(value, p.Value))
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = orderCandidate(item, get(schema, "items"))
		}
		return out
	default:
		return v
	}
}
func matchesBranch(v, schema any) bool {
	if c := get(schema, "const"); c != nil {
		return c == v
	}
	if props, ok := get(schema, "properties").(*Object); ok {
		for _, p := range props.Members() {
			if c := get(p.Value, "const"); c != nil && get(v, p.Key) != c {
				return false
			}
		}
		return true
	}
	return false
}
