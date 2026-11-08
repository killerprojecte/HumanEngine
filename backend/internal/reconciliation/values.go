package reconciliation

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
func deepEqual(a, b any) bool {
	switch x := a.(type) {
	case *Object:
		y, ok := b.(*Object)
		if !ok || x == nil || y == nil {
			return x == nil && ok && y == nil
		}
		if x.Len() != y.Len() {
			return false
		}
		for _, m := range x.Members() {
			v, ok := y.Get(m.Key)
			if !ok || !deepEqual(m.Value, v) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !deepEqual(v, y[i]) {
				return false
			}
		}
		return true
	case float64:
		y, ok := b.(float64)
		return ok && ((math.IsNaN(x) && math.IsNaN(y)) || (x == y && (x != 0 || math.Signbit(x) == math.Signbit(y))))
	default:
		return a == b
	}
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
