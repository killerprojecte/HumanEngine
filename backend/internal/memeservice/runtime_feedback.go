package memeservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"strings"
)

// FeedbackFromSnapshots computes exact source evidence and writes its dependent
// feedback on one owned session. Captured messages are trusted runtime inputs,
// never tool arguments. hasSnapshots distinguishes absent from empty snapshots.
func (s *Service) FeedbackFromSnapshots(ctx context.Context, c Context, id, messageID string, positive bool, messages []*Object, hasSnapshots bool, sources *sourceevents.Store) (out *Object, e error) {
	if c.Simulation {
		return fields("status", "rejected", "reason", "simulation_does_not_learn"), nil
	}
	if sources == nil {
		return nil, errors.New("meme_runtime_sources_required")
	}
	copies := make([]*Object, len(messages))
	for i, m := range messages {
		copies[i] = copyObject(m)
	}
	e = s.transaction(ctx, func(p *scope) error {
		bound := sources.WithSession(p.ws)
		if !hasSnapshots {
			registered := false
			e := bound.Read(ctx, func(v *sourceevents.View) error {
				m, e := v.Registered(c.SceneID, messageID)
				registered = m != nil
				return e
			})
			if e != nil {
				return e
			}
			if registered {
				out = fields("status", "rejected", "reason", "source_snapshot_unavailable")
				return nil
			}
		}
		sender := ""
		for _, m := range copies {
			if get(m, "id") == messageID {
				sender = str(get(m, "senderId"))
				break
			}
		}
		source := fields("sceneId", c.SceneID, "messageId", messageID, "senderId", sender)
		c.SourceEvidence = []*Object{}
		for _, m := range copies {
			if get(m, "origin") == "simulation" {
				continue
			}
			texts := []string{}
			for _, part := range arr(get(m, "parts")) {
				p := obj(part)
				if get(p, "type") == "text" {
					texts = append(texts, str(get(p, "text")))
				}
			}
			available := get(m, "status") == "available" && get(m, "sourceEventRegistrationLimited") != true
			if available {
				var e error
				available, e = bound.CurrentRuntimeSnapshot(ctx, m)
				if e != nil {
					return e
				}
			}
			millis, valid, e := s.policy.DateParser.ParseMillis(str(get(m, "sentAt")))
			if e != nil {
				return e
			}
			var timestamp any
			if valid {
				timestamp = float64(millis)
			}
			c.SourceEvidence = append(c.SourceEvidence, fields("sceneId", get(m, "sceneId"), "messageId", get(m, "id"), "senderId", get(m, "senderId"), "origin", get(m, "origin"), "text", strings.Join(texts, "\n"), "timestamp", timestamp, "available", available, "isBot", get(m, "senderId") == "bot"))
		}
		if e := p.recordFeedback(c, id, source, positive); e != nil {
			return e
		}
		rows, e := p.ws.Query("SELECT positive FROM meme_feedback WHERE asset_id=? AND scene_id=? AND message_id=?", id, c.SceneID, messageID)
		if e != nil {
			return e
		}
		var direction int
		if !rows.Next() {
			rows.Close()
			return errors.New("feedback_receipt_unavailable")
		}
		e = rows.Scan(&direction)
		rows.Close()
		if e != nil {
			return e
		}
		if direction != 1 && direction != -1 {
			return errors.New("feedback_receipt_unavailable")
		}
		if (direction == 1) != positive {
			out = fields("status", "rejected", "reason", "feedback_direction_conflict")
			return nil
		}
		asset, e := p.asset(id)
		if e != nil {
			return e
		}
		out = fields("ok", true, "actionTaken", "ranking_adjusted", "assetId", id, "scope", "asset", "sourceMessageId", messageID, "positive", direction == 1, "preference", get(asset, "preference"), "disabled", get(asset, "disabled"))
		return nil
	})
	return
}
