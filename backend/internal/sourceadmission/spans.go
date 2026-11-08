package sourceadmission

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// KnowledgeSource is the stricter graph-facade boundary. A registered source
// MUST carry its exact event ref here even before its first edit. Ordinary
// assertion SourceAdmission deliberately retains the legacy unedited path.
func (a *Admission) KnowledgeSource(ctx *Object, messageID string) (*Object, error) {
	if e := a.reader.CheckScope(); e != nil {
		return nil, e
	}
	var evidence any
	for _, e := range arr(get(ctx, "sourceEvidence")) {
		if get(e, "sceneId") == get(ctx, "sceneId") && get(e, "messageId") == messageID {
			evidence = e
			break
		}
	}
	if !truthy(get(evidence, "available")) || truthy(get(evidence, "isBot")) || !truthy(get(evidence, "parts")) {
		return nil, nil
	}
	source := obj("sceneId", get(evidence, "sceneId"), "messageId", messageID, "senderId", get(evidence, "senderId"))
	if truthy(get(evidence, "eventRef")) {
		_ = source.Set("eventRef", clone(get(evidence, "eventRef")))
	}
	if !has(source, "eventRef") {
		events, e := a.reader.AuthoritySourceEvents()
		if e != nil {
			return nil, e
		}
		registered, e := events.Registered(str(get(ctx, "sceneId")), messageID)
		if e != nil || registered != nil {
			return nil, e
		}
	}
	ok, e := a.authority.DomainSourceCurrent(source)
	if e != nil || !ok {
		return nil, e
	}
	return obj("source", source, "parts", clone(get(evidence, "parts"))), nil
}

// KnowledgeSpans takes a schema-normalized proposal and authorized current
// backing proofs. It persists offsets/digests, never transient source text.
func (a *Admission) KnowledgeSpans(ctx, proposal *Object, proofs []any) ([]any, error) {
	if e := a.reader.CheckScope(); e != nil {
		return nil, e
	}
	out := []any{}
	for _, span := range arr(get(proposal, "sourceSpans")) {
		actual, e := a.KnowledgeSource(ctx, str(get(span, "messageId")))
		if e != nil {
			return nil, e
		}
		parts := arr(get(actual, "parts"))
		index, start, end := int(num(get(span, "partIndex"))), int(num(get(span, "start"))), int(num(get(span, "end")))
		var part any
		if index >= 0 && index < len(parts) {
			part = parts[index]
		}
		units, e := jsjson.UTF16(str(get(part, "text")))
		if e != nil {
			return nil, e
		}
		authorized := false
		for _, proof := range proofs {
			for _, s := range arr(get(proof, "sources")) {
				if deepEqual(s, get(actual, "source")) {
					authorized = true
					break
				}
			}
		}
		if actual == nil || get(get(actual, "source"), "sceneId") != get(ctx, "sceneId") || part == nil || get(part, "type") != "text" || start < 0 || start >= end || end > len(units) || !authorized {
			return nil, errors.New("knowledge_span_unavailable")
		}
		boundary := func(n int) bool {
			return n > 0 && n < len(units) && units[n-1] >= 0xD800 && units[n-1] <= 0xDBFF && units[n] >= 0xDC00 && units[n] <= 0xDFFF
		}
		if boundary(start) || boundary(end) {
			return nil, errors.New("knowledge_span_surrogate_boundary")
		}
		if get(span, "stance") == "self" && get(get(proposal, "subject"), "kind") == "self" && get(get(actual, "source"), "senderId") != get(get(proposal, "subject"), "personId") {
			return nil, errors.New("knowledge_stance_sender_mismatch")
		}
		root, e := digest([]any{get(ctx, "sceneId"), get(get(actual, "source"), "messageId")})
		if e != nil {
			return nil, e
		}
		text := jsjson.FromUTF16(units[start:end])
		hash, e := digest(text)
		if e != nil {
			return nil, e
		}
		out = append(out, obj("source", clone(get(actual, "source")), "rootId", root, "partIndex", get(span, "partIndex"), "start", get(span, "start"), "end", get(span, "end"), "spanHash", hash, "stance", get(span, "stance")))
	}
	return out, nil
}
