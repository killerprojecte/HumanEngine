// Package habitservice implements live, soft learned expression and scene hints.
// Its records retain JavaScript property order and WTF-8 for durable identity.
package habitservice

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"strings"
	"time"
)

type Object = jsjson.Object

func obj(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func get(o *Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func str(o *Object, k string) string { v, _ := get(o, k).(string); return v }
func object(v any) *Object           { o, _ := v.(*Object); return o }
func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case nil:
		return 0
	case bool:
		if n {
			return 1
		}
		return 0
	}
	return math.NaN()
}
func number(o *Object, k string) float64 { return num(get(o, k)) }
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
func finite(v any) bool { n, ok := v.(float64); return ok && !math.IsNaN(n) && !math.IsInf(n, 0) }
func arr(v any) []any   { a, _ := v.([]any); return a }
func requiredSources(o *Object) ([]any, error) {
	a, ok := get(o, "sources").([]any)
	if !ok {
		return nil, errors.New("habit_sources_invalid")
	}
	return a, nil
}
func clone(o *Object) (*Object, error) {
	if o == nil {
		return nil, nil
	}
	raw, e := jsjson.Stringify(o)
	if e != nil {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	return v.(*Object), nil
}
func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func hash(v any) (string, error) {
	s, e := jsjson.Stringify(v)
	if e != nil {
		return "", e
	}
	return digest(s), nil
}
func sourceKey(o *Object) (string, error) {
	return jsjson.Stringify([]any{get(o, "sceneId"), get(o, "messageId")})
}
func sqlText(s string) string { v, _ := jsjson.UTF8Text(s); return v }
func trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r == 0x9 || r == 0xa || r == 0xb || r == 0xc || r == 0xd || r == 0x20 || r == 0xa0 || r == 0x1680 || r >= 0x2000 && r <= 0x200a || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
	})
}
func uuid() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func iso(ms float64) (string, error) {
	if !finite(ms) || math.Abs(ms) > 8640000000000000 {
		return "", errors.New("Invalid time value")
	}
	t := time.UnixMilli(int64(ms)).UTC()
	y := t.Year()
	if y >= 0 && y <= 9999 {
		return t.Format("2006-01-02T15:04:05.000Z"), nil
	}
	sign := "+"
	if y < 0 {
		sign = "-"
		y = -y
	}
	return sign + leftYear(y) + t.Format("-01-02T15:04:05.000Z"), nil
}
func leftYear(y int) string {
	b := [6]byte{}
	for i := 5; i >= 0; i-- {
		b[i] = byte(y%10) + '0'
		y /= 10
	}
	return string(b[:])
}
func rejected(reason string) *Object { return obj("status", "rejected", "reason", reason) }
func saved(record *Object) *Object   { return obj("status", "saved", "habit", sanitize(record)) }
func sanitize(record *Object) *Object {
	visible := record.Clone()
	visible.Delete("authorWatermarks")
	if str(record, "kind") == "botExpression" {
		_ = visible.Set("sources", []any{})
	}
	return visible
}
func parseRecord(raw string) (*Object, error) {
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	o := object(v)
	if o == nil {
		return nil, errors.New("habit_record_invalid")
	}
	return o, nil
}
func marksMembers(o *Object) []jsjson.Member {
	if o == nil {
		return nil
	}
	return o.Members()
}

func validDirection(v any) bool { n, ok := v.(float64); return ok && (n == 1 || n == -1) }
