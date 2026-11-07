package commitments

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"regexp"
	"strconv"
)

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
func boolean(v any) (any, error) {
	if _, ok := v.(bool); !ok {
		return nil, ErrSchema
	}
	return v, nil
}
func literalOne(v any) (any, error) {
	if v != float64(1) {
		return nil, ErrSchema
	}
	return v, nil
}
func nullable(d decoder) decoder {
	return func(v any) (any, error) {
		if v == nil {
			return nil, nil
		}
		return d(v)
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

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func hashValue(v any) (any, error) {
	s, ok := v.(string)
	if !ok || !hashPattern.MatchString(s) {
		return nil, ErrSchema
	}
	return s, nil
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

func refValue(v any) (any, error) {
	return shape(v, f("id", idValue), f("version", safe(1)), f("deletionEpoch", safe(0)))
}
func sourceEvent(v any) (any, error) {
	return shape(v, f("eventId", idValue), f("revision", safe(1)), f("deletionEpoch", safe(0)))
}
func source(v any) (any, error) {
	return shape(v, f("sceneId", idValue), f("messageId", idValue), f("senderId", idValue), opt("eventRef", sourceEvent))
}
func observation(v any) (any, error) {
	return shape(v, f("sceneId", idValue), f("messageId", idValue), f("senderId", idValue), opt("eventRef", sourceEvent), opt("sentAt", instant), opt("receivedAt", instant))
}
func witness(v any) (any, error) {
	return shape(v, f("kind", enumeration("self", "mentioned", "legacy_sender", "unresolved")), opt("personId", idValue), opt("sourceMessageId", idValue), opt("mentionPartIndex", safe(0)))
}
func proofValue(v any) (any, error) {
	o, e := shape(v, f("proofId", idValue), f("sources", list(source, 1, 64)), f("parents", list(idValue, 0, 64)), opt("parentReferences", list(refValue, 0, 64)), opt("subjectWitness", witness), opt("sourceObservations", list(observation, 0, 64)), opt("invalidatedBy", idValue))
	if e != nil {
		return nil, e
	}
	if assertionauthority.ParentBindingState(o) == "invalid" {
		return nil, ErrSchema
	}
	return o, nil
}
func evidenceValue(v any) (any, error) {
	return shape(v, f("backing", refValue), opt("qualificationRef", refValue), f("backingFingerprint", hashValue), f("proofs", list(proofValue, 1, 64)), opt("personId", idValue), f("kind", enumeration("proposal", "confirm", "complete", "cancel")))
}
func proposalValue(v any) (any, error) {
	o, e := evidenceValue(v)
	if e != nil {
		return nil, e
	}
	if get(o, "kind") != "proposal" {
		return nil, ErrSchema
	}
	return o, nil
}
func groups(d decoder, min int) decoder {
	return func(v any) (any, error) {
		o, e := list(list(d, min, 64), 0, 64)(v)
		if e != nil {
			return nil, e
		}
		n := 0
		for _, g := range arr(o) {
			n += len(arr(g))
		}
		if n > 64 {
			return nil, ErrSchema
		}
		return o, nil
	}
}
func termsValue(v any) (any, error) {
	o, e := shape(v, f("kind", enumeration("plan")), f("title", bounded(1, 500)), f("participantIds", list(idValue, 1, 8)), f("temporalScope", enumeration("current", "past", "future", "unspecified")), f("polarity", enumeration("positive", "negative")), f("modality", enumeration("asserted", "possible")), f("conditions", list(bounded(1, 2000), 0, 8)), opt("timeQualification", bounded(1, 500)), opt("occurredAt", instant), opt("validFrom", instant), opt("validUntil", instant))
	if e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	for _, id := range arr(get(o, "participantIds")) {
		s := str(id)
		if seen[s] {
			return nil, ErrSchema
		}
		seen[s] = true
	}
	if has(o, "validFrom") && has(o, "validUntil") {
		a, ok, e := utcDate.ParseMillis(str(get(o, "validFrom")))
		if e != nil || !ok {
			return nil, ErrSchema
		}
		b, ok, e := utcDate.ParseMillis(str(get(o, "validUntil")))
		if e != nil || !ok || a > b {
			return nil, ErrSchema
		}
	}
	return o, nil
}
func terminal(v any) (any, error) {
	return shape(v, f("version", safe(1)), f("state", enumeration("done", "cancelled")))
}
func headValue(v any) (any, error) { return NormalizeHead(v) }
func NormalizeHead(v any) (*Object, error) {
	o, e := shape(v, f("id", idValue), f("sceneId", idValue), f("latestVersion", safe(1)), f("activeVersion", nullable(safe(1))), f("everActivatedVersion", nullable(safe(1))), f("everTerminal", nullable(terminal)), f("deletionEpoch", safe(0)), f("mutationVersion", safe(1)), f("deleted", boolean))
	if e != nil {
		return nil, e
	}
	latest := num(get(o, "latestVersion"))
	if num(get(o, "activeVersion")) > latest || num(get(o, "everActivatedVersion")) > latest || num(get(get(o, "everTerminal"), "version")) > latest {
		return nil, ErrSchema
	}
	return o, nil
}
func NormalizeRevision(v any) (*Object, error) {
	return shape(v, f("ref", refValue), f("sceneId", idValue), f("terms", termsValue), f("recordedAt", instant), f("evidence", list(evidenceValue, 1, 64)), f("requiredProposalGroups", groups(proposalValue, 1)))
}
func stateValue(v any) (any, error) {
	return shape(v, f("ref", refValue), f("mutationVersion", safe(1)), f("sceneId", idValue), opt("termsHash", hashValue), opt("termsRecordedAt", instant), f("confirmation", enumeration("proposed", "confirmed", "incomplete")), f("lifecycle", enumeration("pending", "done", "cancelled", "needs_review")), f("qualification", enumeration("declared_not_verified")), f("sourceBodyCoverage", enumeration("unavailable")), f("changed", boolean), f("confirmedParticipantIds", list(idValue, 0, 8)), f("missingParticipantIds", list(idValue, 0, 8)), f("completedParticipantIds", list(idValue, 0, 8)), f("cancelledParticipantIds", list(idValue, 0, 8)), f("evidence", list(evidenceValue, 0, 64)), f("requiredProposalGroups", groups(evidenceValue, 0)))
}
func NormalizeTransition(v any) (*Object, error) {
	o, e := shape(v, f("schemaVersion", literalOne), f("id", idValue), f("sceneId", idValue), f("mutationVersion", safe(1)), f("recordedAt", instant), f("reason", enumeration("propose", "amend", "confirm", "complete", "cancel", "purge", "requalify")), f("head", headValue), f("states", nullable(list(stateValue, 0, 2))))
	if e != nil {
		return nil, e
	}
	h := get(o, "head")
	if get(o, "id") != get(h, "id") || get(o, "sceneId") != get(h, "sceneId") || get(o, "mutationVersion") != get(h, "mutationVersion") || get(h, "deleted") == true {
		return nil, ErrSchema
	}
	seen := map[float64]bool{}
	for _, s := range arr(get(o, "states")) {
		r := get(s, "ref")
		version := num(get(r, "version"))
		if get(r, "id") != get(o, "id") || get(s, "sceneId") != get(o, "sceneId") || get(r, "deletionEpoch") != get(h, "deletionEpoch") || get(s, "mutationVersion") != get(o, "mutationVersion") || version > num(get(h, "latestVersion")) || seen[version] {
			return nil, ErrSchema
		}
		seen[version] = true
	}
	return o, nil
}
func NormalizeWindow(v any) (*Object, error) {
	return shape(v, f("schemaVersion", literalOne), f("id", idValue), f("sceneId", idValue), f("firstMutationVersion", safe(1)), f("firstRecordedAt", instant), f("prunedThroughMutationVersion", safe(0)), f("clockNonmonotonic", boolean))
}
func NormalizeMarker(v any) (*Object, error) {
	o, e := shape(v, f("recordedAt", instant), f("head", headValue), f("reason", enumeration("purge", "requalify")))
	if e != nil {
		return nil, e
	}
	if get(get(o, "head"), "deleted") == true {
		return nil, ErrSchema
	}
	return o, nil
}
