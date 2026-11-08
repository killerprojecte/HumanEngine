// Package sourceadmission implements trusted preparation, not assertion storage
// or graph admission. Objects retain JavaScript field order and absent fields.
package sourceadmission

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"regexp"
	"strings"
)

type Preparation struct {
	OK                 bool
	Reason             string
	Context, Candidate *Object
}

func PrepareKnowledge(ctx, candidate *Object) (Preparation, error) {
	reject := func(s string) (Preparation, error) { return Preparation{Reason: s}, nil }
	raw, ok := get(candidate, "knowledge").([]any)
	if !ok || len(raw) < 1 || len(raw) > 3 {
		return reject("knowledge_proposal_invalid")
	}
	parsed := []any{}
	reported := false
	for _, v := range raw {
		p, e := knowledge.ParseProposal(v)
		if e != nil {
			return reject("knowledge_proposal_invalid")
		}
		parsed = append(parsed, p)
		for _, span := range arr(get(p, "sourceSpans")) {
			if get(span, "stance") == "reported" || get(span, "stance") == "quoted" {
				reported = true
			}
		}
	}
	if truthy(get(candidate, "commitment")) || len(arr(get(candidate, "relations"))) > 0 || truthy(get(candidate, "mergeIntoId")) || truthy(get(candidate, "supersedesId")) {
		return reject("knowledge_mixed_write_denied")
	}
	ep := str(get(candidate, "epistemicKind"))
	if reported && (get(candidate, "kind") != "claim" || (ep != "reported" && ep != "inferred" && ep != "hypothesis")) {
		return reject("knowledge_stance_mismatch")
	}
	c := clone(candidate).(*Object)
	_ = c.Set("knowledge", parsed)
	if get(ctx, "provenance") != "contextual" {
		return Preparation{OK: true, Context: ctx, Candidate: c}, nil
	}
	entities, edges := get(ctx, "exposedKnowledgeEntities"), get(ctx, "exposedKnowledgeEdges")
	if entities == nil {
		entities = []any{}
	}
	if edges == nil {
		edges = []any{}
	}
	hash, e := digest([]any{get(ctx, "sceneId"), get(candidate, "sources"), parsed, entities, edges})
	if e != nil {
		return Preparation{}, e
	}
	context := clone(ctx).(*Object)
	_ = context.Set("provenance", "source-only")
	_ = context.Set("dependencyMemoryIds", []any{})
	_ = context.Set("dependencyParentReferences", []any{})
	_ = context.Set("internalDeclaration", "knowledge_declaration")
	_ = c.Set("content", "Model-extracted structured knowledge declaration; reference "+hash+". Not independently verified.")
	_ = c.Set("kind", "claim")
	return Preparation{OK: true, Context: context, Candidate: c}, nil
}

var transportTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T.*(?:Z|[+-][0-9]{2}:[0-9]{2})$`)

// TrustedEvidence accepts normalized runtime messages only. Callers must never
// use model-provided objects as this transport input. Nontext parts keep slots.
func TrustedEvidence(messages []*Object, recalled func(string, string) (bool, error)) ([]any, error) {
	out := []any{}
	for _, m := range messages {
		if get(m, "origin") != "live" {
			continue
		}
		parts, mentions, texts := []any{}, []any{}, []string{}
		for i, p := range arr(get(m, "parts")) {
			if get(p, "type") == "text" {
				parts = append(parts, obj("type", "text", "text", get(p, "text")))
				texts = append(texts, str(get(p, "text")))
			} else {
				parts = append(parts, obj("type", "other"))
			}
			if get(p, "type") == "mention" && truthy(get(p, "userId")) {
				mentions = append(mentions, obj("partIndex", float64(i), "userId", get(p, "userId"), "isBot", get(p, "userId") == "bot"))
			}
		}
		e := obj("origin", "live", "sceneId", get(m, "sceneId"), "messageId", get(m, "id"), "senderId", get(m, "senderId"), "parts", parts, "sourceText", strings.Join(texts, "\n"))
		if truthy(get(m, "sourceEventRef")) {
			_ = e.Set("eventRef", clone(get(m, "sourceEventRef")))
		}
		available := get(m, "status") == "available" && !truthy(get(m, "sourceEventRegistrationLimited"))
		if available {
			r, err := recalled(str(get(m, "sceneId")), str(get(m, "id")))
			if err != nil {
				return nil, err
			}
			available = !r
		}
		_ = e.Set("available", available)
		_ = e.Set("isBot", get(m, "senderId") == "bot")
		_ = e.Set("mentions", mentions)
		for _, key := range []string{"sentAt", "receivedAt"} {
			s := str(get(m, key))
			if transportTime.MatchString(s) && jsdate.FiniteDateParse(s) {
				_ = e.Set(key, s)
			}
		}
		out = append(out, e)
	}
	return out, nil
}
