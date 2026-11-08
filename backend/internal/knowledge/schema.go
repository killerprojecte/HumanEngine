package knowledge

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"regexp"
	"strconv"
)

var ErrSchema = errors.New("knowledge_storage_inconsistent")

type decoder func(any) (any, error)
type field struct {
	name     string
	decode   decoder
	optional bool
}

func f(name string, d decoder) field   { return field{name, d, false} }
func opt(name string, d decoder) field { return field{name, d, true} }
func shape(v any, fields ...field) (*Object, error) {
	o := object(v)
	if o == nil {
		return nil, ErrSchema
	}
	keys := map[string]bool{}
	out := new(Object)
	for _, f := range fields {
		keys[f.name] = true
		x, present := o.Get(f.name)
		if !present {
			if f.optional {
				continue
			}
			return nil, ErrSchema
		}
		x, e := f.decode(x)
		if e != nil {
			return nil, e
		}
		_ = out.Set(f.name, x)
	}
	for _, k := range o.Keys() {
		if !keys[k] {
			return nil, ErrSchema
		}
	}
	return out, nil
}
func bounded(min, max int) decoder {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, ErrSchema
		}
		n, e := jsjson.CodePointCount(s)
		if e != nil || n < min || n > max {
			return nil, ErrSchema
		}
		return s, nil
	}
}

var idValue = bounded(1, 512)

func safe(min float64) decoder {
	return func(v any) (any, error) {
		n, ok := v.(float64)
		if !ok || math.IsInf(n, 0) || math.IsNaN(n) || n < min || n > 9007199254740991 || math.Trunc(n) != n {
			return nil, ErrSchema
		}
		return n, nil
	}
}
func enumeration(values ...string) decoder {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if ok {
			for _, candidate := range values {
				if s == candidate {
					return s, nil
				}
			}
		}
		return nil, ErrSchema
	}
}
func list(d decoder, min, max int) decoder {
	return func(v any) (any, error) {
		a, ok := v.([]any)
		if !ok || len(a) < min || len(a) > max {
			return nil, ErrSchema
		}
		out := make([]any, len(a))
		for i, x := range a {
			n, e := d(x)
			if e != nil {
				return nil, e
			}
			out[i] = n
		}
		return out, nil
	}
}

var instantPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

func instant(v any) (any, error) {
	s, ok := v.(string)
	if !ok || !instantPattern.MatchString(s) {
		return nil, ErrSchema
	}
	y, _ := strconv.Atoi(s[:4])
	m, _ := strconv.Atoi(s[5:7])
	day, _ := strconv.Atoi(s[8:10])
	if m < 1 || m > 12 || day < 1 {
		return nil, ErrSchema
	}
	days := [12]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		days[1] = 29
	}
	if day > days[m-1] {
		return nil, ErrSchema
	}
	return s, nil
}

var utcDate, _ = jsdate.New("UTC")

func ref(v any) (any, error) {
	return shape(v, f("id", idValue), f("version", safe(1)), f("deletionEpoch", safe(0)))
}
func source(v any) (any, error) {
	return shape(v, f("sceneId", idValue), f("messageId", idValue), f("senderId", idValue), opt("eventRef", eventRef))
}
func eventRef(v any) (any, error) {
	return shape(v, f("eventId", idValue), f("revision", safe(1)), f("deletionEpoch", safe(0)))
}
func observation(v any) (any, error) {
	return shape(v, f("sceneId", idValue), f("messageId", idValue), f("senderId", idValue), opt("eventRef", eventRef), opt("sentAt", instant), opt("receivedAt", instant))
}
func witness(v any) (any, error) {
	return shape(v, f("kind", enumeration("self", "mentioned", "legacy_sender", "unresolved")), opt("personId", idValue), opt("sourceMessageId", idValue), opt("mentionPartIndex", safe(0)))
}
func proof(v any) (any, error) {
	o, e := shape(v, f("proofId", idValue), f("sources", list(source, 0, 128)), f("parents", list(idValue, 0, 128)), opt("parentReferences", list(ref, 0, 128)), opt("subjectWitness", witness), opt("sourceObservations", list(observation, 0, 128)), opt("invalidatedBy", idValue))
	if e != nil {
		return nil, e
	}
	parents := map[string]bool{}
	for _, p := range arr(get(o, "parents")) {
		id := str(p)
		if parents[id] {
			return nil, ErrSchema
		}
		parents[id] = true
	}
	if has(o, "parentReferences") {
		refs := arr(get(o, "parentReferences"))
		if len(refs) != len(parents) {
			return nil, ErrSchema
		}
		seen := map[string]bool{}
		for _, r := range refs {
			id := str(get(r, "id"))
			if !parents[id] || seen[id] {
				return nil, ErrSchema
			}
			seen[id] = true
		}
	}
	return o, nil
}
func entity(v any) (any, error) {
	return shape(v, f("id", idValue), f("sceneId", idValue), f("kind", enumeration("plan", "event", "topic", "tool", "organization", "concept", "task")), f("title", bounded(1, 500)), f("backing", ref), f("originEdgeId", idValue))
}
func finite(v any) (any, error) {
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, ErrSchema
	}
	return n, nil
}
func boolean(v any) (any, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return nil, ErrSchema
}
func unit(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	return bounded(1, 80)(v)
}
func endpoint(v any) (any, error) {
	switch get(v, "kind") {
	case "person":
		return shape(v, f("kind", enumeration("person")), f("personId", idValue))
	case "entity":
		return shape(v, f("kind", enumeration("entity")), f("entity", entity))
	case "literal":
		switch get(v, "valueType") {
		case "text":
			return shape(v, f("kind", enumeration("literal")), f("valueType", enumeration("text")), f("value", bounded(1, 2000)))
		case "number":
			return shape(v, f("kind", enumeration("literal")), f("valueType", enumeration("number")), f("value", finite), f("unit", unit))
		case "boolean":
			return shape(v, f("kind", enumeration("literal")), f("valueType", enumeration("boolean")), f("value", boolean))
		}
	}
	return nil, ErrSchema
}

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func hash(v any) (any, error) {
	s, ok := v.(string)
	if !ok || !hashPattern.MatchString(s) {
		return nil, ErrSchema
	}
	return s, nil
}
func one(v any) (any, error) {
	n, ok := v.(float64)
	if !ok || n != 1 {
		return nil, ErrSchema
	}
	return n, nil
}
func span(v any) (any, error) {
	return shape(v, f("source", source), f("rootId", hash), f("partIndex", safe(0)), f("start", safe(0)), f("end", safe(1)), f("spanHash", hash), f("stance", enumeration("self", "asserted", "reported", "quoted")))
}
func dependency(v any) (any, error) {
	return shape(v, f("entity", entity), f("backingFingerprint", hash), f("proofs", list(proof, 1, 128)))
}
func readDependency(v any) (any, error) {
	o, e := shape(v, opt("edgeId", idValue), opt("edgeVersion", safe(1)), f("backing", ref), f("backingFingerprint", hash), f("proofs", list(proof, 1, 128)))
	if e != nil {
		return nil, e
	}
	if has(o, "edgeId") != has(o, "edgeVersion") {
		return nil, ErrSchema
	}
	return o, nil
}
func endpointKind(v any) string {
	switch get(v, "kind") {
	case "entity":
		return str(get(get(v, "entity"), "kind"))
	case "literal":
		return "literal:" + str(get(v, "valueType"))
	}
	return "person"
}
func contains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

// ValidatePredicate is registry version 1, not an open-ended predicate registry.
func ValidatePredicate(predicate string, subject, object *Object, property any) error {
	type rule struct{ subjects, objects []string }
	rules := map[string]rule{
		"participates_in": {[]string{"person"}, []string{"plan", "event"}},
		"prefers_topic":   {[]string{"person"}, []string{"topic", "literal:text"}},
		"uses_tool":       {[]string{"person"}, []string{"tool", "literal:text"}},
		"member_of":       {[]string{"person"}, []string{"organization"}},
		"part_of":         {[]string{"concept", "task"}, []string{"concept", "task"}},
		"depends_on":      {[]string{"concept", "task"}, []string{"concept", "task"}},
		"has_property":    {[]string{"tool", "concept", "task"}, []string{"literal:text", "literal:number", "literal:boolean"}},
	}
	r, ok := rules[predicate]
	if !ok || !contains(r.subjects, endpointKind(subject)) || !contains(r.objects, endpointKind(object)) {
		return errors.New("knowledge_endpoint_type")
	}
	if predicate == "has_property" {
		valid := false
		if get(object, "kind") == "literal" {
			switch property {
			case "version":
				valid = get(object, "valueType") == "text"
			case "enabled":
				valid = get(object, "valueType") == "boolean"
			case "count":
				_, e := safe(0)(get(object, "value"))
				valid = get(object, "valueType") == "number" && e == nil && get(object, "unit") == nil
			case "duration":
				n, ok := get(object, "value").(float64)
				valid = get(object, "valueType") == "number" && ok && n >= 0 && contains([]string{"seconds", "minutes", "hours"}, str(get(object, "unit")))
			}
		}
		if !valid {
			return errors.New("knowledge_property_type")
		}
	}
	return nil
}

// ValidateEdge preserves source strictness, including nested optional presence.
// Numeric/list limits here are the existing TS persisted schema, not new caps.
func ValidateEdge(v *Object) error { _, e := parseEdge(v); return e }
func parseEdge(v *Object) (*Object, error) {
	o, e := shape(v, f("id", idValue), f("version", safe(1)), f("sceneId", idValue), f("registryVersion", one), f("predicate", enumeration("participates_in", "prefers_topic", "uses_tool", "member_of", "part_of", "depends_on", "has_property")), opt("property", enumeration("version", "count", "duration", "enabled")), f("subject", endpoint), f("object", endpoint), f("polarity", enumeration("positive", "negative")), f("modality", enumeration("asserted", "possible")), f("temporalScope", enumeration("current", "past", "future", "unspecified")), f("conditions", list(bounded(1, 2000), 0, 8)), opt("validFrom", instant), opt("validUntil", instant), f("backing", ref), f("backingFingerprint", hash), f("proofs", list(proof, 1, 128)), f("spans", list(span, 1, 8)), f("dependencies", list(dependency, 0, 2)), f("readDependencies", list(readDependency, 0, 168)), f("status", enumeration("candidate", "accepted")), f("admission", enumeration("qualified_candidate", "self_explicit_no_visible_conflict", "direct_assertion_no_visible_conflict")), f("admissionPolicyVersion", one), f("createdAt", instant))
	if e != nil {
		return nil, e
	}
	if (get(o, "predicate") == "has_property") != has(o, "property") {
		return nil, ErrSchema
	}
	if ValidatePredicate(str(get(o, "predicate")), object(get(o, "subject")), object(get(o, "object")), get(o, "property")) != nil {
		return nil, ErrSchema
	}
	if has(o, "validFrom") && has(o, "validUntil") {
		a, _, _ := utcDate.ParseMillis(str(get(o, "validFrom")))
		b, _, _ := utcDate.ParseMillis(str(get(o, "validUntil")))
		if a > b {
			return nil, ErrSchema
		}
	}
	for _, s := range arr(get(o, "spans")) {
		root, e := digest([]any{get(o, "sceneId"), get(get(s, "source"), "messageId")})
		if e != nil || !deepEqual(get(get(s, "source"), "sceneId"), get(o, "sceneId")) || get(s, "rootId") != root || num(get(s, "start")) >= num(get(s, "end")) || num(get(s, "end")) > 1000000 || num(get(s, "partIndex")) > 4095 {
			return nil, ErrSchema
		}
		found := false
		for _, p := range arr(get(o, "proofs")) {
			for _, source := range arr(get(p, "sources")) {
				if deepEqual(source, get(s, "source")) {
					found = true
				}
			}
		}
		if !found {
			return nil, ErrSchema
		}
	}
	for _, point := range []any{get(o, "subject"), get(o, "object")} {
		if get(point, "kind") != "entity" {
			continue
		}
		en := get(point, "entity")
		if !deepEqual(get(en, "sceneId"), get(o, "sceneId")) {
			return nil, ErrSchema
		}
		if deepEqual(get(en, "originEdgeId"), get(o, "id")) {
			if !deepEqual(get(en, "backing"), get(o, "backing")) {
				return nil, ErrSchema
			}
		} else {
			found := false
			for _, d := range arr(get(o, "dependencies")) {
				if deepEqual(get(d, "entity"), en) {
					found = true
				}
			}
			if !found {
				return nil, ErrSchema
			}
		}
	}
	for _, d := range arr(get(o, "dependencies")) {
		if !deepEqual(get(get(d, "entity"), "sceneId"), get(o, "sceneId")) {
			return nil, ErrSchema
		}
	}
	return o, nil
}
