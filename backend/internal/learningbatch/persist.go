package learningbatch

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
	"math"
	"time"
)

func (b *batch) persist(calls []call, p *Progress) (bool, error) {
	p.Totals.CandidateCount = len(calls)
	for _, name := range []string{"habits.propose", "learning.propose"} {
		for _, c := range calls {
			if b.ctx.Err() != nil || b.r.d.Disposed() {
				return false, b.ctx.Err()
			}
			enabled, e := b.r.d.Reads.Enabled(b.ctx, b.scene)
			if e != nil {
				return false, e
			}
			if !enabled {
				return false, nil
			}
			if c.Name != name {
				continue
			}
			schema := candidateName(b.settings)
			if name == "habits.propose" {
				schema = "habit"
			}
			v, e := b.r.schemas.parse(c.Arguments, schema)
			if e != nil {
				p.Totals.RejectedCount++
				b.r.log("warn", "learning_candidate_invalid")
				continue
			}
			p.Applying = true
			p.WritePending = true
			result, e := b.observedPersistence(func() (proposalResult, error) {
				if e := b.assert(); e != nil {
					return proposalResult{}, e
				}
				if name == "habits.propose" {
					return b.proposeHabit(v)
				}
				result, e := b.proposeSource(v)
				if e == nil {
					e = b.assert()
				}
				return result, e
			})
			if e != nil {
				return false, e
			}
			p.WritePending = false
			switch result.status {
			case "saved":
				p.Totals.SavedCount++
			case "rejected":
				p.Totals.RejectedCount++
			default:
				p.Totals.IgnoredCount++
			}
		}
	}
	return true, nil
}
func (b *batch) proposeSource(v *Object) (proposalResult, error) {
	if b.ctx.Err() != nil {
		return proposalResult{"rejected", "request_cancelled"}, nil
	}
	// Engine.propose reparses against the current feature settings, after the
	// batch's captured schema. A settings change cannot grant stale authority.
	settings, e := b.r.d.Reads.Settings(b.ctx)
	if e != nil {
		return proposalResult{}, e
	}
	v, e = b.r.schemas.parse(v, candidateName(settings))
	if e != nil {
		return proposalResult{}, e
	}
	for _, m := range b.messages {
		if !contains(arr(get(v, "sourceMessageIds")), get(m, "id")) {
			continue
		}
		ok, e := b.r.d.Reads.CurrentSource(b.ctx, m)
		if e != nil {
			return proposalResult{}, e
		}
		if get(m, "sourceEventRegistrationLimited") == true || !ok {
			return proposalResult{"rejected", "source_event_changed"}, nil
		}
	}
	if (get(v, "subject") != nil || get(v, "commitment") != nil) && get(v, "publicTechnicalDomain") != nil {
		return proposalResult{"rejected", "personal_subject_not_public_knowledge"}, nil
	}
	evidence, e := sourceadmission.TrustedEvidence(b.messages, func(scene, id string) (bool, error) { return b.r.d.Reads.Recalled(b.ctx, scene, id) })
	if e != nil {
		return proposalResult{}, e
	}
	input := obj("sceneId", b.scene, "simulation", false, "provenance", "source-only", "botUserId", "bot", "sourceEvidence", evidence)
	candidate := withSources(v, b.scene, evidence)
	attribution := sourceadmission.ResolveSubject(input, candidate)
	if get(attribution, "ok") != true {
		return proposalResult{"rejected", str(get(attribution, "reason"))}, nil
	}
	for _, k := range []string{"personId", "kind", "epistemicKind"} {
		if x, ok := attribution.Get(k); ok {
			_ = candidate.Set(k, x)
		} else {
			candidate.Delete(k)
		}
	}
	bad, e := b.r.d.Reads.UnsupportedPronouns(b.ctx, b.scene, candidate, evidence)
	if e != nil {
		return proposalResult{}, e
	}
	if bad {
		return proposalResult{"rejected", "unsupported_personal_pronoun"}, nil
	}
	options := settings.Proposal
	options.GraphEnabled = settings.GraphEnabled
	options.KnowledgeEnabled = settings.KnowledgeEnabled
	result, e := b.r.d.Memory.Propose(b.ctx, input, candidate, options)
	if e != nil {
		return proposalResult{}, e
	}
	if result.Status == "saved" {
		_, e = b.r.d.Reads.AfterSaved(b.ctx, b.scene, candidate, result.Memory, b.messages)
		if e != nil {
			return proposalResult{}, e
		}
	}
	return proposalResult{result.Status, result.Reason}, nil
}
func (b *batch) proposeHabit(v *Object) (proposalResult, error) {
	candidate := obj("kind", get(v, "kind"))
	if get(v, "kind") == "sceneRule" {
		_ = candidate.Set("content", get(v, "content"))
	} else {
		_ = candidate.Set("preference", get(v, "preference"))
		_ = candidate.Set("direction", get(v, "direction"))
	}
	sources := []any{}
	for _, id := range arr(get(v, "sourceMessageIds")) {
		sender := ""
		for _, m := range b.messages {
			if get(m, "id") == id {
				sender = str(get(m, "senderId"))
				break
			}
		}
		sources = append(sources, obj("sceneId", b.scene, "messageId", id, "senderId", sender))
	}
	_ = candidate.Set("sources", sources)
	evidence := []any{}
	for _, m := range b.messages {
		if get(m, "origin") == "simulation" {
			continue
		}
		available := get(m, "status") == "available" && get(m, "sourceEventRegistrationLimited") != true
		if available {
			var e error
			available, e = b.r.d.Reads.CurrentSource(b.ctx, m)
			if e != nil {
				return proposalResult{}, e
			}
		}
		timestamp := math.NaN()
		if t, e := time.Parse(time.RFC3339Nano, str(get(m, "sentAt"))); e == nil {
			timestamp = float64(t.UnixMilli())
		}
		evidence = append(evidence, obj("sceneId", get(m, "sceneId"), "messageId", get(m, "id"), "senderId", get(m, "senderId"), "origin", get(m, "origin"), "text", messageText(m, "\n"), "timestamp", timestamp, "available", available, "isBot", get(m, "senderId") == "bot"))
	}
	result, e := b.r.d.Habits.Propose(b.ctx, habitservice.Context{SceneID: b.scene, SourceEvidence: evidence}, candidate)
	return proposalResult{str(get(result, "status")), str(get(result, "reason"))}, e
}
