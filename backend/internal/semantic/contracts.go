package semantic

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"regexp"
	"strconv"
)

var ErrContract = errors.New("semantic_contract_invalid")

type scDecoder func(any) (any, error)
type scField struct {
	name     string
	decode   scDecoder
	optional bool
}

func scF(name string, d scDecoder) scField   { return scField{name, d, false} }
func scOpt(name string, d scDecoder) scField { return scField{name, d, true} }
func scShape(v any, fields ...scField) (*jsjson.Object, error) {
	o := scObject(v)
	if o == nil {
		return nil, ErrContract
	}
	keys := map[string]bool{}
	out := new(jsjson.Object)
	for _, scF := range fields {
		keys[scF.name] = true
		x, present := o.Get(scF.name)
		if !present {
			if scF.optional {
				continue
			}
			return nil, ErrContract
		}
		x, e := scF.decode(x)
		if e != nil {
			return nil, e
		}
		_ = out.Set(scF.name, x)
	}
	for _, k := range o.Keys() {
		if !keys[k] {
			return nil, ErrContract
		}
	}
	return out, nil
}
func scBounded(min, max int) scDecoder {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, ErrContract
		}
		n, e := jsjson.CodePointCount(s)
		if e != nil || n < min || n > max {
			return nil, ErrContract
		}
		return s, nil
	}
}

var scIdValue = scBounded(1, 512)

func scSafe(min float64) scDecoder {
	return func(v any) (any, error) {
		n, ok := v.(float64)
		if !ok || math.IsInf(n, 0) || math.IsNaN(n) || n < min || n > 9007199254740991 || math.Trunc(n) != n {
			return nil, ErrContract
		}
		return n, nil
	}
}
func scEnumeration(values ...string) scDecoder {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if ok {
			for _, candidate := range values {
				if s == candidate {
					return s, nil
				}
			}
		}
		return nil, ErrContract
	}
}
func scList(d scDecoder, min, max int) scDecoder {
	return func(v any) (any, error) {
		a, ok := v.([]any)
		if !ok || len(a) < min || len(a) > max {
			return nil, ErrContract
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

var scInstantPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)

func scInstant(v any) (any, error) {
	s, ok := v.(string)
	if !ok || !scInstantPattern.MatchString(s) {
		return nil, ErrContract
	}
	y, _ := strconv.Atoi(s[:4])
	m, _ := strconv.Atoi(s[5:7])
	day, _ := strconv.Atoi(s[8:10])
	if m < 1 || m > 12 || day < 1 {
		return nil, ErrContract
	}
	days := [12]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		days[1] = 29
	}
	if day > days[m-1] {
		return nil, ErrContract
	}
	return s, nil
}

func scRef(v any) (any, error) {
	return scShape(v, scF("id", scIdValue), scF("version", scSafe(1)), scF("deletionEpoch", scSafe(0)))
}
func scSource(v any) (any, error) {
	return scShape(v, scF("sceneId", scIdValue), scF("messageId", scIdValue), scF("senderId", scIdValue), scOpt("eventRef", scEventRef))
}
func scEventRef(v any) (any, error) {
	return scShape(v, scF("eventId", scIdValue), scF("revision", scSafe(1)), scF("deletionEpoch", scSafe(0)))
}
func scObservation(v any) (any, error) {
	return scShape(v, scF("sceneId", scIdValue), scF("messageId", scIdValue), scF("senderId", scIdValue), scOpt("eventRef", scEventRef), scOpt("sentAt", scInstant), scOpt("receivedAt", scInstant))
}
func scWitness(v any) (any, error) {
	return scShape(v, scF("kind", scEnumeration("self", "mentioned", "legacy_sender", "unresolved")), scOpt("personId", scIdValue), scOpt("sourceMessageId", scIdValue), scOpt("mentionPartIndex", scSafe(0)))
}
func scProof(v any) (any, error) {
	o, e := scShape(v, scF("proofId", scIdValue), scF("sources", scList(scSource, 0, 128)), scF("parents", scList(scIdValue, 0, 128)), scOpt("parentReferences", scList(scRef, 0, 128)), scOpt("subjectWitness", scWitness), scOpt("sourceObservations", scList(scObservation, 0, 128)), scOpt("invalidatedBy", scIdValue))
	if e != nil {
		return nil, e
	}
	parents := map[string]bool{}
	for _, p := range scArr(scGet(o, "parents")) {
		id := scStr(p)
		if parents[id] {
			return nil, ErrContract
		}
		parents[id] = true
	}
	if scHas(o, "parentReferences") {
		refs := scArr(scGet(o, "parentReferences"))
		if len(refs) != len(parents) {
			return nil, ErrContract
		}
		seen := map[string]bool{}
		for _, r := range refs {
			id := scStr(scGet(r, "id"))
			if !parents[id] || seen[id] {
				return nil, ErrContract
			}
			seen[id] = true
		}
	}
	return o, nil
}
func scFinite(v any) (any, error) {
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, ErrContract
	}
	return n, nil
}
func scBoolean(v any) (any, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return nil, ErrContract
}
func scUnit(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	return scBounded(1, 80)(v)
}
func scEndpoint(v any) (any, error) {
	switch scGet(v, "kind") {
	case "person":
		return scShape(v, scF("kind", scEnumeration("person")), scF("personId", scIdValue))
	case "entity":
		return scShape(v, scF("kind", scEnumeration("entity")), scF("entityId", scIdValue), scF("entityKind", scEnumeration("plan", "event", "topic", "tool", "organization", "concept", "task")), scF("backing", scRef), scF("originEdgeId", scIdValue))
	case "literal":
		switch scGet(v, "valueType") {
		case "text":
			return scShape(v, scF("kind", scEnumeration("literal")), scF("valueType", scEnumeration("text")), scF("value", scBounded(1, 2000)))
		case "number":
			return scShape(v, scF("kind", scEnumeration("literal")), scF("valueType", scEnumeration("number")), scF("value", scFinite), scF("unit", scUnit))
		case "boolean":
			return scShape(v, scF("kind", scEnumeration("literal")), scF("valueType", scEnumeration("boolean")), scF("value", scBoolean))
		}
	}
	return nil, ErrContract
}

var scHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func scHash(v any) (any, error) {
	s, ok := v.(string)
	if !ok || !scHashPattern.MatchString(s) {
		return nil, ErrContract
	}
	return s, nil
}
func scOne(v any) (any, error) {
	n, ok := v.(float64)
	if !ok || n != 1 {
		return nil, ErrContract
	}
	return n, nil
}

func scObject(v any) *jsjson.Object { o, _ := v.(*jsjson.Object); return o }
func scGet(v any, k string) any {
	if o := scObject(v); o != nil {
		x, _ := o.Get(k)
		return x
	}
	return nil
}
func scHas(v any, k string) bool {
	if o := scObject(v); o != nil {
		_, ok := o.Get(k)
		return ok
	}
	return false
}
func scArr(v any) []any  { a, _ := v.([]any); return a }
func scStr(v any) string { s, _ := v.(string); return s }
func scNullable(d scDecoder) scDecoder {
	return func(v any) (any, error) {
		if v == nil {
			return nil, nil
		}
		return d(v)
	}
}
func scSelection(v any) (any, error) {
	return scShape(v, scF("ref", scRef), scF("dependencyKey", scHash))
}
func scParent(v any) (any, error) {
	return scShape(v, scF("ref", scRef), scF("dependencyKey", scHash), scF("fingerprint", scHash), scF("proofs", scList(scProof, 1, 128)), scF("roots", scList(scHash, 1, 128)))
}
func scAnchorDependency(v any) (any, error) {
	return scShape(v, scF("ref", scRef), scF("fingerprint", scHash), scF("proofs", scList(scProof, 1, 128)))
}
func scAnchor(v any) (any, error) { return scShape(v, scF("id", scIdValue), scF("version", scSafe(1))) }
func scQualification(v any) (any, error) {
	return scShape(v,
		scF("edgeId", scIdValue), scF("edgeVersion", scSafe(1)), scF("registryVersion", scOne),
		scF("predicate", scEnumeration("participates_in", "prefers_topic", "uses_tool", "member_of", "part_of", "depends_on", "has_property")),
		scOpt("property", scEnumeration("version", "count", "duration", "enabled")),
		scF("subject", scEndpoint), scF("object", scEndpoint), scF("polarity", scEnumeration("positive", "negative")), scF("modality", scEnumeration("asserted", "possible")),
		scF("temporalScope", scEnumeration("current", "past", "future", "unspecified")), scF("conditions", scList(scBounded(1, 2000), 0, 8)),
		scF("occurredAt", scNullable(scInstant)), scF("validFrom", scNullable(scInstant)), scF("validUntil", scNullable(scInstant)),
		scF("status", scEnumeration("candidate", "accepted")), scF("admission", scEnumeration("qualified_candidate", "self_explicit_no_visible_conflict", "direct_assertion_no_visible_conflict")),
		scF("interpretation", scEnumeration("attributed_proposed_association_not_observed_event")))
}

// ValidateCandidate rebuilds all objects in SemanticCandidateSchema field order.
// This order is significant: semanticHash hashes JSON.stringify of Zod's output.
// It implements only schema refinements, not admission or current-authority rules.
func ValidateCandidate(v *jsjson.Object) (*jsjson.Object, error) {
	o, e := scShape(v,
		scF("schemaVersion", scOne), scF("id", scIdValue), scF("sceneId", scIdValue), scF("personId", scNullable(scIdValue)), scF("partitionKey", scNullable(scHash)), scF("version", scSafe(1)), scF("inputKey", scHash),
		scF("kind", scEnumeration("summary_candidate", "duplicate_candidate", "episode_candidate")), scF("text", scBounded(1, 6000)), scF("selectedParentIds", scList(scIdValue, 2, 12)), scF("parents", scList(scParent, 2, 12)),
		scOpt("episodeAnchor", scAnchor), scOpt("episodeMembershipKey", scHash), scF("anchorDependencies", scList(scAnchorDependency, 0, 128)), scF("anchorQualifications", scList(scQualification, 0, 40)), scF("hypothesis", scBoolean),
		scF("status", scEnumeration("candidate", "admitted", "rejected")), scF("admission", scEnumeration("qualified_candidate", "structural_low_risk_summary", "administrator_review")), scF("createdAt", scInstant), scF("applicableUntil", scNullable(scInstant)))
	if e != nil {
		return nil, e
	}
	ids := map[string]bool{}
	for _, p := range scArr(scGet(o, "parents")) {
		id := scStr(scGet(scGet(p, "ref"), "id"))
		if ids[id] {
			return nil, ErrContract
		}
		ids[id] = true
	}
	selected := map[string]bool{}
	for _, p := range scArr(scGet(o, "selectedParentIds")) {
		id := scStr(p)
		if !ids[id] || selected[id] {
			return nil, ErrContract
		}
		selected[id] = true
	}
	episode := scGet(o, "kind") == "episode_candidate"
	if episode != (scGet(o, "partitionKey") == nil) || episode != (len(scArr(scGet(o, "anchorQualifications"))) > 0) || episode != (len(scArr(scGet(o, "anchorDependencies"))) > 0) || episode != scHas(o, "episodeMembershipKey") || episode != scHas(o, "episodeAnchor") {
		return nil, ErrContract
	}
	edges := map[string]bool{}
	for _, q := range scArr(scGet(o, "anchorQualifications")) {
		id := scStr(scGet(q, "edgeId"))
		if edges[id] {
			return nil, ErrContract
		}
		edges[id] = true
	}
	return o, nil
}

// ValidateAttempt preserves the source's independent attempt capacity policies.
func ValidateAttempt(v *jsjson.Object) (*jsjson.Object, error) {
	return scShape(v, scF("schemaVersion", scOne), scF("sceneId", scIdValue), scF("inputKey", scHash), scF("status", scEnumeration("started", "finished", "abandoned")), scF("version", scSafe(1)), scF("parents", scList(scSelection, 0, 12)), scF("roots", scList(scHash, 0, 1536)))
}
func ParseCandidate(raw string) (*jsjson.Object, error) {
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	return ValidateCandidate(scObject(v))
}
func ParseAttempt(raw string) (*jsjson.Object, error) {
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	return ValidateAttempt(scObject(v))
}
