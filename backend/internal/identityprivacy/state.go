package identityprivacy

import (
	"errors"
	"math"
	"regexp"
	"strconv"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

var errCorrupt = errors.New("identity_registry_corrupt")
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var instantPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

// strict also restores the schema's property order, as Zod object parsing does.
// Hashing the raw input's property order would create an incompatible witness.
func strict(v any, keys ...string) (*jsjson.Object, bool) {
	o, ok := v.(*jsjson.Object)
	if !ok || len(o.Members()) != len(keys) {
		return nil, false
	}
	out := new(jsjson.Object)
	for _, k := range keys {
		v, ok := o.Get(k)
		if !ok {
			return nil, false
		}
		_ = out.Set(k, v)
	}
	return out, true
}
func bounded(v any, max int) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	n, e := jsjson.UTF16Len(s)
	return e == nil && n >= 1 && n <= max
}
func integer(v any) bool {
	n, ok := v.(float64)
	return ok && !math.IsNaN(n) && n >= 0 && n <= 9007199254740991 && math.Trunc(n) == n
}
func endpoint(v any) (*jsjson.Object, bool) {
	o, ok := strict(v, "adapterId", "accountId", "personId")
	if !ok {
		return nil, false
	}
	return o, bounded(get(o, "adapterId"), 160) && bounded(get(o, "accountId"), 160) && bounded(get(o, "personId"), 160)
}
func endpointKey(v *jsjson.Object) string {
	s, _ := jsjson.Stringify([]any{get(v, "adapterId"), get(v, "accountId"), get(v, "personId")})
	return s
}
func instant(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	if !ok || !instantPattern.MatchString(s) {
		return false
	}
	y, _ := strconv.Atoi(s[:4])
	m, _ := strconv.Atoi(s[5:7])
	d, _ := strconv.Atoi(s[8:10])
	if m < 1 || m > 12 || d < 1 {
		return false
	}
	days := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		days[1] = 29
	}
	return d <= days[m-1]
}
func alias(v any) (*jsjson.Object, bool) {
	o, ok := strict(v, "id", "endpoint", "label", "scopeSceneId", "source", "validFrom", "validUntil")
	if !ok || !bounded(get(o, "id"), 160) || !bounded(get(o, "label"), 200) || !bounded(get(o, "scopeSceneId"), 160) || !instant(get(o, "validFrom")) || !instant(get(o, "validUntil")) {
		return nil, false
	}
	ep, ok := endpoint(get(o, "endpoint"))
	if !ok {
		return nil, false
	}
	_ = o.Set("endpoint", ep)
	source, ok := get(o, "source").(*jsjson.Object)
	if !ok {
		return nil, false
	}
	switch get(source, "kind") {
	case "manual":
		source, ok = strict(source, "kind", "administratorId")
		if !ok || !bounded(get(source, "administratorId"), 160) {
			return nil, false
		}
	case "observation":
		keys := []string{"kind", "sceneId", "messageId", "senderId"}
		ref, has := source.Get("eventRef")
		if has {
			keys = append(keys, "eventRef")
		}
		source, ok = strict(source, keys...)
		if !ok || !bounded(get(source, "sceneId"), 160) || !bounded(get(source, "messageId"), 160) || !bounded(get(source, "senderId"), 160) || get(source, "sceneId") != get(o, "scopeSceneId") {
			return nil, false
		}
		if has {
			r, e := sourceevents.ParseRef(ref)
			if e != nil {
				return nil, false
			}
			_ = source.Set("eventRef", r.Value())
		}
	default:
		return nil, false
	}
	_ = o.Set("source", source)
	if from, until := get(o, "validFrom"), get(o, "validUntil"); from != nil && until != nil {
		parser, e := jsdate.New("UTC")
		if e != nil {
			return nil, false
		}
		a, va, ea := parser.ParseMillis(from.(string))
		b, vb, eb := parser.ParseMillis(until.(string))
		if ea != nil || eb != nil || !va || !vb || a >= b {
			return nil, false
		}
	}
	return o, true
}
func parseState(v any) (*jsjson.Object, error) {
	o, ok := strict(v, "format", "revision", "edges", "aliases", "operations")
	if !ok || get(o, "format") != float64(1) || !integer(get(o, "revision")) {
		return nil, errCorrupt
	}
	edges, a := get(o, "edges").([]any)
	aliases, b := get(o, "aliases").([]any)
	ops, c := get(o, "operations").([]any)
	if !a || !b || !c || len(edges) > 4096 || len(aliases) > 4096 || len(ops) > 16384 || len(edges)+len(aliases)+len(ops) > 16384 || get(o, "revision") != float64(len(ops)) {
		return nil, errCorrupt
	}
	safeEdges := []any{}
	seenEdges := map[string]bool{}
	graph := map[string][]string{}
	for _, raw := range edges {
		pair, ok := raw.([]any)
		if !ok || len(pair) != 2 {
			return nil, errCorrupt
		}
		left, a := endpoint(pair[0])
		right, b := endpoint(pair[1])
		if !a || !b {
			return nil, errCorrupt
		}
		lk, rk := endpointKey(left), endpointKey(right)
		if lk == rk {
			return nil, errCorrupt
		}
		first, last := lk, rk
		if first > last {
			first, last = last, first
		}
		key, _ := jsjson.Stringify([]any{first, last})
		if seenEdges[key] {
			return nil, errCorrupt
		}
		seenEdges[key] = true
		graph[lk] = append(graph[lk], rk)
		graph[rk] = append(graph[rk], lk)
		safeEdges = append(safeEdges, []any{left, right})
	}
	visited := map[string]bool{}
	for start := range graph {
		if visited[start] {
			continue
		}
		pending := []string{start}
		count := 0
		for len(pending) > 0 {
			at := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if visited[at] {
				continue
			}
			visited[at] = true
			count++
			pending = append(pending, graph[at]...)
		}
		if count > 32 {
			return nil, errCorrupt
		}
	}
	safeAliases := []any{}
	seenAliases := map[string]bool{}
	for _, raw := range aliases {
		a, ok := alias(raw)
		if !ok {
			return nil, errCorrupt
		}
		id := get(a, "id").(string)
		if seenAliases[id] {
			return nil, errCorrupt
		}
		seenAliases[id] = true
		safeAliases = append(safeAliases, a)
	}
	safeOps := []any{}
	seenOps := map[string]bool{}
	for i, raw := range ops {
		op, ok := strict(raw, "id", "hash", "revision")
		if !ok || !bounded(get(op, "id"), 160) || !integer(get(op, "revision")) || get(op, "revision") != float64(i+1) {
			return nil, errCorrupt
		}
		digest, ok := get(op, "hash").(string)
		if !ok || !hashPattern.MatchString(digest) {
			return nil, errCorrupt
		}
		id := get(op, "id").(string)
		if seenOps[id] {
			return nil, errCorrupt
		}
		seenOps[id] = true
		safeOps = append(safeOps, op)
	}
	_ = o.Set("edges", safeEdges)
	_ = o.Set("aliases", safeAliases)
	_ = o.Set("operations", safeOps)
	return o, nil
}
