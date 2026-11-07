package botruntime

import (
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"time"
)

type Object = jsjson.Object

func obj(kv ...any) *Object {
	o, _ := jsjson.NewObject()
	for i := 0; i < len(kv); i += 2 {
		if err := o.Set(kv[i].(string), kv[i+1]); err != nil {
			panic(err)
		}
	}
	return o
}
func get(v any, k string) any {
	if o, ok := v.(*Object); ok && o != nil {
		x, _ := o.Get(k)
		return x
	}
	return nil
}
func str(v any) string  { s, _ := v.(string); return s }
func array(v any) []any { a, _ := v.([]any); return a }
func stringsOf(v any) []string {
	a := []string{}
	for _, x := range array(v) {
		a = append(a, str(x))
	}
	return a
}
func stringArray(a []string) []any {
	r := make([]any, len(a))
	for i, s := range a {
		r[i] = s
	}
	return r
}
func objects(a []*Object) []any {
	r := make([]any, len(a))
	for i, s := range a {
		r[i] = s
	}
	return r
}
func cloneJSON(v any) any {
	switch x := v.(type) {
	case *Object:
		r := obj()
		for _, m := range x.Members() {
			_ = r.Set(m.Key, cloneJSON(m.Value))
		}
		return r
	case []any:
		r := make([]any, len(x))
		for i, e := range x {
			r[i] = cloneJSON(e)
		}
		return r
	default:
		return x
	}
}
func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}
func unique(a []string) []string {
	r := []string{}
	seen := map[string]bool{}
	for _, x := range a {
		if !seen[x] {
			r = append(r, x)
			seen[x] = true
		}
	}
	return r
}
func remove(a []string, s string) []string {
	r := []string{}
	for _, x := range a {
		if x != s {
			r = append(r, x)
		}
	}
	return r
}
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	default:
		return true
	}
}
func code(s string) error { return errors.New(s) }
func rawParts(m any) []json.RawMessage {
	r := []json.RawMessage{}
	for _, p := range array(get(m, "parts")) {
		s, e := jsjson.Stringify(p)
		if e != nil {
			panic(e)
		}
		r = append(r, json.RawMessage(s))
	}
	return r
}
func fromRawParts(parts []json.RawMessage) []any {
	r := []any{}
	for _, p := range parts {
		v, e := jsjson.ParseBytes(p)
		if e != nil {
			panic(e)
		}
		r = append(r, v)
	}
	return r
}
func iso(ms float64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000Z")
}
func idOf(m *Object) string { return str(get(m, "id")) }
func filterMessages(a []*Object, fn func(*Object) bool) []*Object {
	r := []*Object{}
	for _, m := range a {
		if fn(m) {
			r = append(r, m)
		}
	}
	return r
}
func ids(a []*Object) []string {
	r := []string{}
	for _, m := range a {
		r = append(r, idOf(m))
	}
	return r
}
func suffix[T any](a []T, n int) []T {
	if len(a) > n {
		return a[len(a)-n:]
	}
	return a
}
