// Package memeservice owns live image admission and opaque reference identity.
package memeservice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type Object = jsjson.Object

var Categories = []string{"appearance", "style", "subject", "emotion", "intent", "tone", "intensity", "memeMeaning"}

func get(o *Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func str(v any) string               { s, _ := v.(string); return s }
func yes(v any) bool                 { return v == true }
func arr(v any) []any                { a, _ := v.([]any); return a }
func obj(v any) *Object              { o, _ := v.(*Object); return o }
func set(o *Object, k string, v any) { _ = o.Set(k, v) }
func object() *Object                { return new(Object) }
func digest(b []byte) string         { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func identity(v ...any) string       { b, _ := jsjson.Stringify(v); return digest([]byte(b)) }
func copyObject(o *Object) *Object {
	if o == nil {
		return nil
	}
	b, _ := jsjson.Stringify(o)
	v, _ := jsjson.Parse(b)
	return obj(v)
}
func sanitize(o *Object) *Object { n := copyObject(o); set(n, "sources", []any{}); return n }
func content(o *Object) string {
	if h := str(get(o, "contentHash")); h != "" {
		return h
	}
	return str(get(o, "id"))
}
func same(a, b *Object) bool {
	return get(a, "sceneId") == get(b, "sceneId") && get(a, "messageId") == get(b, "messageId") && get(a, "senderId") == get(b, "senderId")
}
func emptyTags() *Object {
	o := object()
	for _, k := range Categories {
		set(o, k, []any{})
	}
	set(o, "ocr", "")
	return o
}
func ValidateTags(v any) (*Object, error) {
	o := obj(v)
	if o == nil {
		return nil, errors.New("invalid_meme_tags")
	}
	n := emptyTags()
	for _, k := range Categories {
		a, ok := get(o, k).([]any)
		if !ok || len(a) > 20 {
			return nil, errors.New("invalid_meme_tags")
		}
		seen := map[string]bool{}
		out := []any{}
		for _, x := range a {
			s, ok := x.(string)
			if !ok || units(s) > 160 {
				return nil, errors.New("invalid_meme_tags")
			}
			if !seen[s] {
				out = append(out, s)
				seen[s] = true
			}
		}
		set(n, k, out)
	}
	s, ok := get(o, "ocr").(string)
	if !ok || units(s) > 4000 {
		return nil, errors.New("invalid_meme_tags")
	}
	set(n, "ocr", s)
	return n, nil
}

// MarshalJSON-compatible helper for tests and adapters without changing JS key order.
func JSON(o *Object) json.RawMessage { s, _ := jsjson.Stringify(o); return json.RawMessage(s) }

func units(s string) int {
	n, e := jsjson.UTF16Len(s)
	if e != nil {
		return int(^uint(0) >> 1)
	}
	return n
}
