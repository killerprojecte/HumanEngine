package assertionproposal

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"strings"
)

// UniqueSources preserves first insertion order and the final value for each
// JSON identity, including eventRef property ordering, just as the source Map.
func UniqueSources(sources []any) ([]any, error) {
	out := []any{}
	positions := map[string]int{}
	for _, s := range sources {
		key, e := jsjson.Stringify([]any{get(s, "sceneId"), get(s, "messageId"), get(s, "senderId"), get(s, "eventRef")})
		if e != nil {
			return nil, e
		}
		next := obj("sceneId", get(s, "sceneId"), "messageId", get(s, "messageId"), "senderId", get(s, "senderId"))
		if truthy(get(s, "eventRef")) {
			v, e := cloneValue(get(s, "eventRef"))
			if e != nil {
				return nil, e
			}
			_ = next.Set("eventRef", v)
		}
		if i, ok := positions[key]; ok {
			out[i] = next
		} else {
			positions[key] = len(out)
			out = append(out, next)
		}
	}
	return out, nil
}
func member(v any, choices ...string) bool {
	for _, s := range choices {
		if v == s {
			return true
		}
	}
	return false
}
func ValidateMemory(m *Object, p *jsdate.Parser) error {
	invalid := errors.New("invalid_memory")
	content, ok := get(m, "content").(string)
	if !ok || content == "" {
		return invalid
	}
	units, e := jsjson.UTF16(content)
	if e != nil {
		return e
	}
	if len(units) > 20000 {
		return invalid
	}
	if _, ok := get(m, "locked").(bool); !ok {
		return invalid
	}
	confidence, ok := get(m, "confidence").(float64)
	if !member(get(m, "status"), "active", "disputed", "superseded", "invalidated") || !member(get(m, "kind"), "fact", "claim", "term", "experience", "summary") || !member(get(m, "importance"), "low", "medium", "high") || !ok || math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
		return invalid
	}
	if has(m, "epistemicKind") && member(get(m, "epistemicKind"), "reported", "inferred", "hypothesis") && get(m, "kind") != "claim" {
		return invalid
	}
	date := func(key string) error {
		if !has(m, key) {
			return nil
		}
		text, ok := get(m, key).(string)
		if !ok {
			return invalid
		}
		_, valid, e := p.ParseMillis(text)
		if e != nil {
			return e
		}
		if !valid {
			return invalid
		}
		return nil
	}
	if e := date("validFrom"); e != nil {
		return e
	}
	if has(m, "validFrom") && has(m, "validUntil") {
		from, _, e := p.ParseMillis(str(get(m, "validFrom")))
		if e != nil {
			return e
		}
		// Non-string until values fail the later date guard; a NaN comparison cannot reject early.
		if until, ok := get(m, "validUntil").(string); ok {
			end, valid, e := p.ParseMillis(until)
			if e != nil {
				return e
			}
			if valid && from > end {
				return invalid
			}
		}
	}
	if has(m, "epistemicKind") && !member(get(m, "epistemicKind"), "explicit", "reported", "observed", "inferred", "hypothesis") {
		return invalid
	}
	if e := date("occurredAt"); e != nil {
		return e
	}
	if e := date("validUntil"); e != nil {
		return e
	}
	sources, ok := get(m, "sources").([]any)
	if !ok {
		return errors.New("assertion_sources_array_required")
	}
	scope := get(m, "scope")
	if get(scope, "kind") == "scene" {
		if !truthy(get(scope, "sceneId")) {
			return errors.New("memory_scope_mismatch")
		}
		for _, s := range sources {
			if get(s, "sceneId") != get(scope, "sceneId") {
				return errors.New("memory_scope_mismatch")
			}
		}
	} else if get(scope, "kind") != "sharedPublicKnowledge" || has(scope, "personId") || has(scope, "sceneId") {
		return errors.New("invalid_public_scope")
	}
	for _, s := range sources {
		if !truthy(get(s, "sceneId")) || !truthy(get(s, "messageId")) || !truthy(get(s, "senderId")) {
			return errors.New("invalid_source")
		}
	}
	return nil
}
func trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r == 0xFEFF || r == 0xA0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200A) || r == 0x2028 || r == 0x2029 || r == 0x202F || r == 0x205F || r == 0x3000 || r == ' ' || (r >= 9 && r <= 13)
	})
}
