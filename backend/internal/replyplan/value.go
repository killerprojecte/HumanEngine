package replyplan

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Object, Array and strings use jsjson's ordered JSON/WTF-8 data model. This is
// essential to the source's JSON.stringify byte budgets and opaque identities.
type Object = jsjson.Object

func object(v any) *Object { o, _ := v.(*Object); return o }
func get(v any, key string) any {
	if o := object(v); o != nil {
		x, _ := o.Get(key)
		return x
	}
	return nil
}
func has(v any, key string) bool {
	if o := object(v); o != nil {
		_, ok := o.Get(key)
		return ok
	}
	return false
}
func str(v any) string    { s, _ := v.(string); return s }
func array(v any) []any   { a, _ := v.([]any); return a }
func isArray(v any) bool  { _, ok := v.([]any); return ok }
func isString(v any) bool { _, ok := v.(string); return ok }
func isBool(v any) bool   { _, ok := v.(bool); return ok }
func isNullOrString(v any, key string) bool {
	return has(v, key) && (get(v, key) == nil || isString(get(v, key)))
}
func member(key string, value any) jsjson.Member { return jsjson.Member{Key: key, Value: value} }
func obj(m ...jsjson.Member) *Object {
	o, err := jsjson.NewObject(m...)
	if err != nil {
		panic(err)
	}
	return o
}
func set(o *Object, k string, v any) {
	if err := o.Set(k, v); err != nil {
		panic(err)
	}
}
func stringsArray(a []string) []any {
	out := make([]any, len(a))
	for i, s := range a {
		out[i] = s
	}
	return out
}
func memberStrings(v any) []string {
	a := array(v)
	out := make([]string, len(a))
	for i, s := range a {
		out[i] = str(s)
	}
	return out
}
func includes(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
func oneOf(v any, values ...string) bool { s, ok := v.(string); return ok && includes(values, s) }
func stringSet(v []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	return m
}
func safeInteger(v any) bool {
	n, ok := v.(float64)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && math.Trunc(n) == n && math.Abs(n) <= 9007199254740991
}
func count(v any) bool         { return safeInteger(v) && v.(float64) >= 0 }
func positiveCount(v any) bool { return count(v) && v.(float64) > 0 }
func truthy(v any) bool {
	switch x := v.(type) {
	case nil, undefinedValue:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	default:
		return true
	}
}
func every(v any, fn func(any) bool) bool {
	a, ok := v.([]any)
	if !ok {
		return false
	}
	for _, x := range a {
		if !fn(x) {
			return false
		}
	}
	return true
}
func some(v any, fn func(any) bool) bool {
	for _, x := range array(v) {
		if fn(x) {
			return true
		}
	}
	return false
}
func unique(a []string) bool    { return len(stringSet(a)) == len(a) }
func errCode(code string) error { return errors.New(code) }

// clone retains -0/NaN and property presence, unlike a JSON encode/decode copy.
func clone(v any) any {
	switch x := v.(type) {
	case *Object:
		o := obj()
		for _, m := range x.Members() {
			set(o, m.Key, clone(m.Value))
		}
		return o
	case []any:
		a := make([]any, len(x))
		for i, item := range x {
			a[i] = clone(item)
		}
		return a
	default:
		return v
	}
}

// deepEqual is node:util isDeepStrictEqual for the supported data model. Object
// property insertion order does not matter; array order and signed zero do.
func deepEqual(a, b any) bool {
	switch x := a.(type) {
	case undefinedValue:
		return isUndefined(b)
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && (math.IsNaN(x) && math.IsNaN(y) || math.Float64bits(x) == math.Float64bits(y))
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !deepEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case *Object:
		y, ok := b.(*Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, m := range x.Members() {
			z, ok := y.Get(m.Key)
			if !ok || !deepEqual(m.Value, z) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
func jsString(v any) (string, error) {
	switch x := v.(type) {
	case undefinedValue:
		return "undefined", nil
	case nil:
		return "null", nil
	case string:
		return x, nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case float64:
		if math.IsNaN(x) {
			return "NaN", nil
		}
		if math.IsInf(x, 1) {
			return "Infinity", nil
		}
		if math.IsInf(x, -1) {
			return "-Infinity", nil
		}
		return jsjson.Stringify(x)
	case []any:
		a := make([]string, len(x))
		for i, e := range x {
			if e != nil && !isUndefined(e) {
				var err error
				a[i], err = jsString(e)
				if err != nil {
					return "", err
				}
			}
		}
		return strings.Join(a, ","), nil
	default:
		// Supported objects have ordinary prototypes and data properties,
		// not callable methods. An own toString shadows Object.prototype's
		// method; valueOf still cannot yield a primitive in this data model.
		if has(v, "toString") {
			return "", errCode("Cannot convert object to primitive value")
		}
		return "[object Object]", nil
	}
}
func propertyString(v any, key string) (string, error) {
	if !has(v, key) {
		return "undefined", nil
	}
	return jsString(get(v, key))
}
func integerString(n int) string { return strconv.Itoa(n) }
