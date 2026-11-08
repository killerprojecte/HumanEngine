package assertionproposal

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
)

type Object = jsjson.Object

func object(v any) *Object { o, _ := v.(*Object); return o }
func get(v any, k string) any {
	if o := object(v); o != nil {
		x, _ := o.Get(k)
		return x
	}
	return nil
}
func has(v any, k string) bool {
	if o := object(v); o != nil {
		_, ok := o.Get(k)
		return ok
	}
	return false
}
func str(v any) string { s, _ := v.(string); return s }
func arr(v any) []any  { a, _ := v.([]any); return a }
func obj(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}
func cloneValue(v any) (any, error) {
	switch x := v.(type) {
	case *Object:
		out := new(Object)
		for _, m := range x.Members() {
			v, e := cloneValue(m.Value)
			if e != nil {
				return nil, e
			}
			_ = out.Set(m.Key, v)
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			c, e := cloneValue(v)
			if e != nil {
				return nil, e
			}
			out[i] = c
		}
		return out, nil
	default:
		return v, nil
	}
}
