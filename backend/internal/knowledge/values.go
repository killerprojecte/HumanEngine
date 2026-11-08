package knowledge

import (
	"crypto/sha256"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"time"
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
func str(v any) string  { s, _ := v.(string); return s }
func num(v any) float64 { n, _ := v.(float64); return n }
func arr(v any) []any   { a, _ := v.([]any); return a }
func obj(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func clone(v any) any {
	switch x := v.(type) {
	case *Object:
		if x == nil {
			return nil
		}
		o := new(Object)
		for _, m := range x.Members() {
			_ = o.Set(m.Key, clone(m.Value))
		}
		return o
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = clone(v)
		}
		return a
	}
	return v
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
func digest(v any) (string, error) {
	raw, e := jsjson.Stringify(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h), nil
}
func isoMillis(ms int64) string {
	t := time.UnixMilli(ms).UTC()
	y := t.Year()
	year := fmt.Sprintf("%04d", y)
	if y < 0 {
		year = fmt.Sprintf("-%06d", -y)
	} else if y > 9999 {
		year = fmt.Sprintf("+%06d", y)
	}
	return fmt.Sprintf("%s-%02d-%02dT%02d:%02d:%02d.%03dZ", year, t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1000000)
}
