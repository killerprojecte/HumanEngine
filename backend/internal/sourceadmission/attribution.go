package sourceadmission

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"math"
	"regexp"
	"strconv"
)

var subjectTime = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.[0-9]+)?(?:Z|[+-]([0-9]{2}):([0-9]{2}))$`)

func trustedTime(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	m := subjectTime.FindStringSubmatch(s)
	if m == nil || !jsdate.FiniteDateParse(s) {
		return false
	}
	n := make([]int, 8)
	for i := range n {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	days := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	if n[0]%4 == 0 && (n[0]%100 != 0 || n[0]%400 == 0) {
		days[1] = 29
	}
	return n[1] >= 1 && n[1] <= 12 && n[2] >= 1 && n[2] <= days[n[1]-1] && n[3] <= 23 && n[4] <= 59 && n[5] <= 59 && n[6] <= 23 && n[7] <= 59
}
func ResolveSubject(ctx, c *Object) *Object {
	reject := func(reason string) *Object { return obj("ok", false, "reason", reason) }
	if truthy(get(ctx, "simulation")) {
		return reject("simulation_does_not_learn")
	}
	if has(c, "subject") {
		for _, k := range []string{"occurredAt", "validFrom", "validUntil"} {
			if has(c, k) && !trustedTime(get(c, k)) {
				return reject("invalid_subject_time")
			}
		}
	}
	bot := get(ctx, "botUserId")
	if bot == nil {
		bot = "bot"
	}
	sources := []any{}
	for _, s := range arr(get(c, "sources")) {
		var found any
		for _, e := range arr(get(ctx, "sourceEvidence")) {
			if get(e, "sceneId") == get(ctx, "sceneId") && sameSource(s, e) && truthy(get(e, "available")) && !truthy(get(e, "isBot")) && get(e, "senderId") != bot {
				found = e
				break
			}
		}
		if found == nil {
			return reject("invalid_source_evidence")
		}
		sources = append(sources, found)
	}
	if len(sources) == 0 {
		return reject("invalid_source_evidence")
	}
	observations := []any{}
	for _, s := range sources {
		o := obj("sceneId", get(s, "sceneId"), "messageId", get(s, "messageId"), "senderId", get(s, "senderId"))
		for _, k := range []string{"sentAt", "receivedAt"} {
			if trustedTime(get(s, k)) {
				_ = o.Set(k, get(s, k))
			}
		}
		observations = append(observations, o)
	}
	var person, witness any
	subject := get(c, "subject")
	kind := get(subject, "kind")
	switch kind {
	case "unresolved":
		if truthy(get(c, "personId")) {
			return reject("subject_person_mismatch")
		}
		witness = obj("kind", "unresolved")
	case "self", "mentioned":
		var source any
		for _, s := range sources {
			if get(s, "messageId") == get(subject, "sourceMessageId") {
				source = s
				break
			}
		}
		if source == nil {
			return reject("subject_source_not_cited")
		}
		if kind == "self" {
			person = get(source, "senderId")
			witness = obj("kind", "self", "personId", person, "sourceMessageId", get(source, "messageId"))
		} else {
			index, ok := get(subject, "mentionPartIndex").(float64)
			person = get(subject, "personId")
			if !ok || math.IsNaN(index) || index < 0 || index > 9007199254740991 || math.Trunc(index) != index || !truthy(person) || person == "all" || person == bot {
				return reject("invalid_subject_mention")
			}
			found := false
			for _, m := range arr(get(source, "mentions")) {
				if get(m, "partIndex") == index && get(m, "userId") == person && !truthy(get(m, "isBot")) {
					found = true
					break
				}
			}
			if !found {
				return reject("subject_mention_not_witnessed")
			}
			witness = obj("kind", "mentioned", "personId", person, "sourceMessageId", get(source, "messageId"), "mentionPartIndex", index)
		}
		if has(c, "personId") && get(c, "personId") != person {
			return reject("subject_person_mismatch")
		}
	default:
		if has(c, "subject") {
			return reject("invalid_subject")
		}
		if truthy(get(c, "personId")) {
			person = get(c, "personId")
			for _, s := range sources {
				if get(s, "senderId") == person {
					witness = obj("kind", "legacy_sender", "personId", person, "sourceMessageId", get(s, "messageId"))
					break
				}
			}
			if witness == nil {
				return reject("legacy_subject_not_cited_sender")
			}
		}
	}
	ep := get(c, "epistemicKind")
	if kind == "mentioned" && ep != "inferred" && ep != "hypothesis" {
		ep = "reported"
	}
	memoryKind := get(c, "kind")
	if kind == "unresolved" || (has(c, "subject") && (ep == "reported" || ep == "inferred" || ep == "hypothesis")) {
		memoryKind = "claim"
	}
	out := obj("ok", true)
	if person != nil {
		_ = out.Set("personId", person)
	}
	_ = out.Set("kind", memoryKind)
	if ep != nil {
		_ = out.Set("epistemicKind", ep)
	}
	if witness != nil {
		_ = out.Set("witness", witness)
	}
	_ = out.Set("sourceObservations", observations)
	return out
}
func sameSource(a, b any) bool {
	return get(a, "sceneId") == get(b, "sceneId") && get(a, "messageId") == get(b, "messageId") && get(a, "senderId") == get(b, "senderId")
}
