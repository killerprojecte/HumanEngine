// Package assertionauthority separates privacy provenance from temporal and
// revision applicability. It does not perform privacy writes or expose an HTTP
// MemoryService. Production reads borrow one assertions.Reader scope.
package assertionauthority

import (
	"errors"
	"math"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type Object = jsjson.Object

func get(value any, key string) any {
	if o, ok := value.(*Object); ok && o != nil {
		v, _ := o.Get(key)
		return v
	}
	return nil
}
func has(value any, key string) bool {
	if o, ok := value.(*Object); ok && o != nil {
		_, present := o.Get(key)
		return present
	}
	return false
}
func str(value any) string     { s, _ := value.(string); return s }
func object(value any) *Object { o, _ := value.(*Object); return o }
func array(value any) ([]any, error) {
	a, ok := value.([]any)
	if !ok {
		return nil, errors.New("assertion_authority_array_required")
	}
	return a, nil
}
func fieldArray(value any, key string) ([]any, error) { return array(get(value, key)) }
func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	}
	return true
}
func safe(value any, minimum float64) bool {
	n, ok := value.(float64)
	return ok && n >= minimum && n <= 9007199254740991 && math.Trunc(n) == n
}
func scopeKey(scope any) string {
	if get(scope, "kind") == "scene" {
		return "scene:" + str(get(scope, "sceneId"))
	}
	return "shared:public"
}
func clone(value any) any {
	switch v := value.(type) {
	case *Object:
		if v == nil {
			return (*Object)(nil)
		}
		o := new(Object)
		for _, m := range v.Members() {
			_ = o.Set(m.Key, clone(m.Value))
		}
		return o
	case []any:
		a := make([]any, len(v))
		for i := range v {
			a[i] = clone(v[i])
		}
		return a
	}
	return value
}
func uniqueSources(sources []any) ([]any, error) {
	out := []any{}
	positions := map[string]int{}
	for _, source := range sources {
		key, e := jsjson.Stringify([]any{get(source, "sceneId"), get(source, "messageId"), get(source, "senderId"), get(source, "eventRef")})
		if e != nil {
			return nil, e
		}
		o := new(Object)
		for _, k := range []string{"sceneId", "messageId", "senderId"} {
			_ = o.Set(k, get(source, k))
		}
		if ref := get(source, "eventRef"); truthy(ref) {
			_ = o.Set("eventRef", clone(ref))
		}
		if i, ok := positions[key]; ok {
			out[i] = o
		} else {
			positions[key] = len(out)
			out = append(out, o)
		}
	}
	return out, nil
}

// ParentBindingState preserves absent (legacy) versus null/invalid bindings.
func ParentBindingState(proof *Object) string {
	parents, e := fieldArray(proof, "parents")
	if e != nil {
		return "invalid"
	}
	ids := map[string]bool{}
	for _, v := range parents {
		id, ok := v.(string)
		if !ok || id == "" || ids[id] {
			return "invalid"
		}
		ids[id] = true
	}
	if !has(proof, "parentReferences") {
		if len(parents) > 0 {
			return "legacy_unknown"
		}
		return "bound"
	}
	refs, e := fieldArray(proof, "parentReferences")
	if e != nil || len(refs) != len(parents) {
		return "invalid"
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		id, ok := get(ref, "id").(string)
		if object(ref) == nil || !ok || !ids[id] || seen[id] || !safe(get(ref, "version"), 1) || !safe(get(ref, "deletionEpoch"), 0) {
			return "invalid"
		}
		seen[id] = true
	}
	return "bound"
}
func parentReference(proof *Object, id string) *Object {
	refs, _ := fieldArray(proof, "parentReferences")
	for _, ref := range refs {
		if get(ref, "id") == id {
			return object(ref)
		}
	}
	return nil
}

// ParentScopeAllows is directional: public parents may support any child;
// private parents require the same scene and a compatible subject.
func ParentScopeAllows(child, parent *Object) bool {
	return get(parent, "kind") == "sharedPublicKnowledge" ||
		get(child, "kind") == "scene" && get(parent, "sceneId") == get(child, "sceneId") &&
			(!has(parent, "personId") || has(child, "personId") && get(parent, "personId") == get(child, "personId"))
}
