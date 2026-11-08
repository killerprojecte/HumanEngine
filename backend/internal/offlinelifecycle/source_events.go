package offlinelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

type sourceReplay struct{ owner *recovery.Maintenance }

func (p *sourceReplay) supported(ctx context.Context, tx *recovery.MaintenanceTx) (bool, error) {
	n, e := schemas(ctx, tx, "source_event_%", map[string][]string{"source_event_heads": {"identity_key", "event_id", "scene_id", "message_id", "value"}, "source_event_revisions": {"event_id", "revision", "value"}, "source_event_edit_keys": {"event_id", "edit_key", "revision", "change_hash"}, "source_event_tombstones": {"scene_id", "message_id"}}, true, "recovery_unknown_source_event_schema")
	if e != nil {
		return false, e
	}
	if n != 0 && n != 4 {
		return false, errors.New("recovery_unknown_source_event_schema")
	}
	return n > 0, nil
}
func (p *sourceReplay) AssertSupported(ctx context.Context) error {
	return p.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error { _, e := p.supported(ctx, tx); return e })
}
func (p *sourceReplay) Recall(ctx context.Context, scene, message string) error {
	return p.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		present, e := p.supported(ctx, tx)
		if e != nil || !present {
			return e
		}
		s, e := sourceevents.NewMaintenanceStore(p.owner, tx, ctx)
		if e != nil {
			return e
		}
		if e = s.RevokeAll(ctx, scene, message); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT value FROM source_event_revisions")
		if e != nil {
			return e
		}
		values := []string{}
		for rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				break
			}
			values = append(values, raw)
		}
		if e = errors.Join(e, rows.Err(), rows.Close()); e != nil {
			return e
		}
		for _, raw := range values {
			v, e := jsjson.Parse(raw)
			if e != nil {
				return e
			}
			m, e := sourceevents.ParseMetadata(v)
			if e != nil {
				return e
			}
			if m.Identity.SceneID == scene && m.Identity.MessageID == message {
				return errors.New("recovery_source_event_purge_incomplete")
			}
		}
		var remaining bool
		if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM source_event_heads WHERE scene_id=? AND message_id=?)", scene, message).Scan(&remaining); e != nil {
			return e
		}
		if remaining {
			return errors.New("recovery_source_event_purge_incomplete")
		}
		return nil
	})
}
func (p *sourceReplay) Verify(ctx context.Context, tx *recovery.MaintenanceTx, events []string) error {
	present, e := p.supported(ctx, tx)
	if e != nil {
		return e
	}
	latest, e := finalEvents(events, func(v *object) string {
		if get(v, "kind") != "source_edit_head" {
			return ""
		}
		key, _ := jsjson.Stringify([]any{get(v, "sceneId"), get(v, "messageId")})
		return key
	})
	if e != nil {
		return e
	}
	for _, v := range latest {
		yes, e := recalled(ctx, tx, str(get(v, "sceneId")), str(get(v, "messageId")))
		if e != nil {
			return e
		}
		if yes {
			continue
		}
		if !present {
			return errors.New("recovery_source_event_material_missing")
		}
		var identity, id, scene, message, raw string
		e = tx.QueryRowContext(ctx, "SELECT identity_key,event_id,scene_id,message_id,value FROM source_event_heads WHERE identity_key=?", get(v, "identityHash")).Scan(&identity, &id, &scene, &message, &raw)
		if errors.Is(e, sql.ErrNoRows) {
			return errors.New("recovery_source_event_material_missing")
		}
		if e != nil {
			return e
		}
		parsed, e := jsjson.Parse(raw)
		if e != nil {
			return e
		}
		head, e := sourceevents.ParseHead(parsed)
		if e != nil {
			return errors.New("recovery_source_event_invalid")
		}
		m := head.Metadata
		key, e := sourceevents.IdentityKey(m.Identity)
		if e != nil {
			return e
		}
		hash, e := sourceevents.HeadHash(m, head.Sequence, head.OrderMode)
		if e != nil {
			return e
		}
		ref := sourceevents.Ref{EventID: str(get(v, "eventId")), Revision: int64(get(v, "revision").(float64)), DeletionEpoch: int64(get(v, "deletionEpoch").(float64))}
		if head.Deleted || id != ref.EventID || scene != get(v, "sceneId") || message != get(v, "messageId") || key != get(v, "identityHash") || m.Identity.SceneID != scene || m.Identity.MessageID != message || m.Ref != ref || hash != get(v, "headHash") {
			return errors.New("recovery_source_event_head_mismatch")
		}
		s, e := sourceevents.NewMaintenanceStore(p.owner, tx, ctx)
		if e != nil {
			return e
		}
		e = s.Read(ctx, func(view *sourceevents.View) error {
			current, e := view.Current(m.Ref, m.Identity)
			if e != nil {
				return e
			}
			if !current.Eligible || current.Metadata == nil {
				return errors.New("recovery_source_event_material_missing")
			}
			h, e := sourceevents.HeadHash(*current.Metadata, head.Sequence, head.OrderMode)
			if e != nil {
				return e
			}
			if h != get(v, "headHash") {
				return errors.New("recovery_source_event_material_missing")
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return nil
}
