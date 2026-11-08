// Package experienceview projects already-authorized snapshots. DTO validation
// is never authority, joint participation, or evidence of observed events.
package experienceview

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"regexp"
)

type Object = jsjson.Object

var ErrInvalid = errors.New("experience_projection_invalid_input")
var qualification = regexp.MustCompile(`^[a-f0-9]{64}$`)

// RegExp.test coerces JSON arrays through Array.toString. Only singleton
// nesting can produce 64 hexadecimal characters; commas, nulls, booleans,
// numbers and ordinary objects cannot match this expression.
func qualified(v any) bool {
	switch x := v.(type) {
	case string:
		return qualification.MatchString(x)
	case []any:
		return len(x) == 1 && qualified(x[0])
	}
	return false
}

func get(v any, k string) any {
	if o, ok := v.(*Object); ok && o != nil {
		x, _ := o.Get(k)
		return x
	}
	return nil
}
func obj(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func text(v any) bool { _, ok := v.(string); return ok }
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
	}
	return true
}

// strictEqual compares JSON scalar values and object identities, not content.
// Separately decoded JSON arrays are distinct objects even when deeply equal.
func strictEqual(a, b any) bool {
	switch x := a.(type) {
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
		return ok && x == y
	case *Object:
		y, ok := b.(*Object)
		return ok && x == y
	}
	return false
}
func safe(v any, min float64) bool {
	n, ok := v.(float64)
	return ok && n >= min && n <= 9007199254740991 && math.Trunc(n) == n
}
func validRef(v any) bool {
	return text(get(v, "id")) && get(v, "id") != "" && safe(get(v, "version"), 1) && safe(get(v, "deletionEpoch"), 0)
}
func key(entry any) (string, error) {
	prefix := "commitment:"
	ref := get(entry, "commitmentRef")
	if get(entry, "kind") == "reported_experience" {
		prefix = "assertion:"
		ref = get(entry, "assertionRef")
	}
	if ref == nil {
		return "", ErrInvalid
	}
	s, e := jsjson.Stringify([]any{get(ref, "id"), get(ref, "version"), get(ref, "deletionEpoch")})
	return prefix + s, e
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
func equal(a, b any) bool {
	switch x := a.(type) {
	case *Object:
		y, ok := b.(*Object)
		if !ok || x == nil || y == nil {
			return ok && x == nil && y == nil
		}
		if x.Len() != y.Len() {
			return false
		}
		for _, m := range x.Members() {
			v, ok := y.Get(m.Key)
			if !ok || !equal(m.Value, v) {
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
			if !equal(v, y[i]) {
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

// Project preserves runtime source validation, insertion order, and all supplied
// record fields. Its recursively cloned output is owned by the caller.
func Project(input *Object) (*Object, error) {
	if input == nil {
		return nil, ErrInvalid
	}
	limit := get(input, "limit")
	if limit == nil {
		limit = float64(12)
	}
	simulation, simOK := get(input, "simulation").(bool)
	assertions, aOK := get(input, "assertions").([]any)
	plans, pOK := get(input, "commitments").([]any)
	coverage := get(input, "upstreamCoverage")
	validCoverage := func(v any) bool { return v == "complete" || v == "limited" }
	if !truthy(get(input, "sceneId")) || !simOK || !safe(limit, 1) || limit.(float64) > 12 || !aOK || !pOK || len(assertions)+len(plans) > 40 || !validCoverage(get(coverage, "assertions")) || !validCoverage(get(coverage, "commitments")) {
		return nil, ErrInvalid
	}
	entries := []any{}
	seen := map[string]bool{}
	appendEntry := func(entry *Object) error {
		k, e := key(entry)
		if e != nil || seen[k] {
			return ErrInvalid
		}
		seen[k] = true
		entries = append(entries, entry)
		return nil
	}
	if !simulation {
		for _, snapshot := range assertions {
			record := get(snapshot, "record")
			ref := get(snapshot, "ref")
			q := get(snapshot, "qualificationKey")
			_, sourcesOK := get(record, "sources").([]any)
			if record == nil || !truthy(get(record, "scope")) || !text(get(record, "content")) || !text(get(record, "learnedAt")) || !sourcesOK || !validRef(ref) || get(ref, "id") != get(record, "id") || !qualified(q) {
				return nil, ErrInvalid
			}
			scope := get(record, "scope")
			status := get(record, "status")
			if get(scope, "kind") == "sharedPublicKnowledge" || truthy(get(record, "internalUse")) || get(record, "kind") != "experience" || (status != "active" && status != "disputed") {
				continue
			}
			if get(scope, "kind") != "scene" || !strictEqual(get(scope, "sceneId"), get(input, "sceneId")) {
				return nil, ErrInvalid
			}
			if e := appendEntry(obj("kind", "reported_experience", "assertionRef", ref, "qualificationKey", q, "record", record, "time", obj("occurredAt", get(record, "occurredAt"), "recordedAt", get(record, "learnedAt")))); e != nil {
				return nil, e
			}
		}
		for _, view := range plans {
			ref := get(view, "ref")
			if !validRef(ref) || !strictEqual(get(view, "sceneId"), get(input, "sceneId")) || !safe(get(view, "mutationVersion"), 1) || get(view, "qualification") != "declared_not_verified" || get(view, "sourceBodyCoverage") != "unavailable" {
				return nil, ErrInvalid
			}
			exposure := ref.(*Object).Clone()
			_ = exposure.Set("mutationVersion", get(view, "mutationVersion"))
			if e := appendEntry(obj("kind", "shared_plan_declaration", "commitmentRef", exposure, "view", view, "completionBasis", "participant_self_report_not_observed_event")); e != nil {
				return nil, e
			}
		}
	}
	reasons := []any{}
	if !simulation {
		if c, ok := coverage.(*Object); ok {
			for _, m := range c.Members() {
				if m.Value == "limited" {
					reasons = append(reasons, "upstream_limited")
					break
				}
			}
		}
	}
	n := int(limit.(float64))
	if len(entries) > n {
		reasons = append(reasons, "entry_limit")
	} else {
		n = len(entries)
	}
	status := "complete"
	if len(reasons) > 0 {
		status = "limited"
	}
	return clone(obj("items", entries[:n], "coverage", obj("status", status, "reasons", reasons, "returned", float64(n), "omittedEntries", float64(len(entries)-n)), "interpretation", "independent_qualified_records_not_synthesized_episodes", "sourceBodyCoverage", "unavailable")).(*Object), nil
}

// ResolveCurrentBatch is an owner-controlled fresh authority boundary. A nil
// result or error denotes stale/unavailable. Verify does not authorize content.
type ResolveCurrentBatch func(sceneID string, references *Object) (*Object, error)

func Verify(sceneID string, page *Object, resolve ResolveCurrentBatch) (valid bool) {
	// Source catches failures from validation, callbacks and projection alike.
	defer func() {
		if recover() != nil {
			valid = false
		}
	}()
	items, ok := get(page, "items").([]any)
	if sceneID == "" || !ok || len(items) > 12 {
		return false
	}
	if len(items) == 0 {
		return true
	}
	seen := map[string]bool{}
	assertions := []any{}
	plans := []any{}
	for _, item := range items {
		k, e := key(item)
		if e != nil || seen[k] {
			return false
		}
		seen[k] = true
		switch get(item, "kind") {
		case "reported_experience":
			assertions = append(assertions, get(item, "assertionRef"))
		case "shared_plan_declaration":
			plans = append(plans, get(item, "commitmentRef"))
		}
	}
	if resolve == nil {
		return false
	}
	current, e := resolve(sceneID, obj("assertions", assertions, "commitments", plans))
	if e != nil || current == nil {
		return false
	}
	a, aOK := get(current, "assertions").([]any)
	p, pOK := get(current, "commitments").([]any)
	if !aOK || !pOK || len(a) != len(assertions) || len(p) != len(plans) {
		return false
	}
	input := obj("sceneId", sceneID, "simulation", false)
	for _, m := range current.Members() {
		_ = input.Set(m.Key, m.Value)
	}
	_ = input.Set("upstreamCoverage", obj("assertions", "complete", "commitments", "complete"))
	_ = input.Set("limit", float64(12))
	projected, e := Project(input)
	if e != nil {
		return false
	}
	fresh := get(projected, "items").([]any)
	if len(fresh) != len(items) {
		return false
	}
	byKey := map[string]any{}
	for _, item := range fresh {
		k, e := key(item)
		if e != nil {
			return false
		}
		byKey[k] = item
	}
	for _, item := range items {
		k, e := key(item)
		if e != nil || !equal(item, byKey[k]) {
			return false
		}
	}
	return true
}
