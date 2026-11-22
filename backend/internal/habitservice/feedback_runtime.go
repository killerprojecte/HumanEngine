package habitservice

import (
	"context"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// FeedbackFromSnapshots resolves only the cited sources, from trusted runtime
// messages. Model-read authorization, source proof and the mutation share the
// same owning writer; source IDs and direction are the only model inputs.
func (s *Service) FeedbackFromSnapshots(ctx context.Context, scene, id string, direction float64, ids []string, messages []*Object, captured bool, events *sourceevents.Store, dates *jsdate.Parser, authorized func(*recovery.WriteSession) bool) (out *Object, err error) {
	cited := make(map[string]*Object, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := cited[id]; !ok {
			cited[id] = nil
			unique = append(unique, id)
		}
	}
	selected := make([]*Object, 0, len(unique))
	for _, m := range messages {
		if id := str(m, "id"); get(m, "sceneId") == scene {
			if original, want := cited[id]; want && original == nil {
				cited[id] = m
				selected = append(selected, m)
			}
		}
	}
	err = s.TransactionWithSession(ctx, func(b *Service, ws *recovery.WriteSession) error {
		if !authorized(ws) {
			return errors.New("memory_read_revoked")
		}
		bound := events.WithSession(ws)
		if !captured {
			missing := false
			e := bound.Read(ctx, func(view *sourceevents.View) error {
				for _, id := range unique {
					ref, e := view.Registered(scene, id)
					if e != nil || ref != nil {
						missing = ref != nil
						return e
					}
				}
				return nil
			})
			if e != nil {
				return e
			}
			if missing {
				out = rejected("source_snapshot_unavailable")
				return nil
			}
		}
		c := Context{SceneID: scene, SourceEvidence: make([]any, 0, len(cited))}
		sources := make([]any, 0, len(ids))
		for _, m := range selected {
			if get(m, "origin") != "live" || get(m, "status") != "available" || get(m, "sourceEventRegistrationLimited") == true || get(m, "senderId") == "bot" {
				continue
			}
			current, e := bound.CurrentRuntimeSnapshot(ctx, m)
			if e != nil {
				return e
			}
			if !current {
				continue
			}
			stamp, valid, e := dates.ParseMillis(str(m, "sentAt"))
			if e != nil {
				return e
			}
			if !valid {
				continue
			}
			var texts []string
			for _, part := range arr(get(m, "parts")) {
				p := object(part)
				if get(p, "type") == "text" {
					texts = append(texts, str(p, "text"))
				}
			}
			c.SourceEvidence = append(c.SourceEvidence, obj("sceneId", scene, "messageId", get(m, "id"), "senderId", get(m, "senderId"), "origin", "live", "text", strings.Join(texts, "\n"), "timestamp", float64(stamp), "available", true, "isBot", false))
		}
		for _, id := range ids {
			sources = append(sources, obj("sceneId", scene, "messageId", id, "senderId", str(cited[id], "senderId")))
		}
		var e error
		out, e = b.feedback(ctx, c, id, direction, sources)
		return e
	})
	if err != nil {
		return nil, err
	}
	return
}
