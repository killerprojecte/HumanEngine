package contract

import (
	"github.com/killerprojecte/HumanEngine/backend/compatibility"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"strings"
)

// ParseOrdered preserves source schema property enumeration and lossless UTF-16
// strings when a parsed configuration is persisted again. Validation remains
// the same schema/refinement path as Parse; no encoding/json string roundtrip.
func (s *Schemas) ParseOrdered(name string, input []byte) (*jsjson.Object, error) {
	values, e := s.Parse(name, input)
	if e != nil {
		return nil, e
	}
	document, e := jsjson.ParseBytes(compatibility.OpenAPI)
	if e != nil {
		return nil, e
	}
	read := func(o any, k string) any { v, _ := o.(*jsjson.Object).Get(k); return v }
	definitions := read(read(document, "components"), "schemas").(*jsjson.Object)
	var convert func(any, any) any
	convert = func(schema any, value any) any {
		spec, _ := schema.(*jsjson.Object)
		if spec != nil {
			if reference, ok := spec.Get("$ref"); ok {
				schema, _ = definitions.Get(strings.TrimPrefix(reference.(string), "#/components/schemas/"))
				spec, _ = schema.(*jsjson.Object)
			}
		}
		switch v := value.(type) {
		case map[string]any:
			out := new(jsjson.Object)
			var properties *jsjson.Object
			if spec != nil {
				p, _ := spec.Get("properties")
				properties, _ = p.(*jsjson.Object)
			}
			if properties != nil {
				for _, m := range properties.Members() {
					if child, ok := v[m.Key]; ok {
						_ = out.Set(m.Key, convert(m.Value, child))
					}
				}
			}
			return out
		case []any:
			out := make([]any, len(v))
			var items any
			if spec != nil {
				items, _ = spec.Get("items")
			}
			for i, child := range v {
				out[i] = convert(items, child)
			}
			return out
		default:
			return value
		}
	}
	schema, _ := definitions.Get(name)
	return convert(schema, values).(*jsjson.Object), nil
}
