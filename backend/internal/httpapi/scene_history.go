package httpapi

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func browserScene(ctx context.Context, q *sql.Conn, id string) (botruntime.Scene, error) {
	var scene botruntime.Scene
	err := q.QueryRowContext(ctx, `SELECT json_extract(value,'$.id'),
		json_extract(value,'$.kind'), json_extract(value,'$.adapterId'),
		json_extract(value,'$.accountId'), json_extract(value,'$.platformSceneId'),
		json_extract(value,'$.name'), json_extract(value,'$.avatarUrl')
		FROM scenes WHERE id=?`, id).Scan(&scene.ID, &scene.Kind, &scene.AdapterID,
		&scene.AccountID, &scene.PlatformSceneID, &scene.DisplayName, &scene.AvatarURL)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return scene, err
}

func sameHistoryScene(a, b botruntime.Scene) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.AdapterID == b.AdapterID &&
		a.AccountID == b.AccountID && a.PlatformSceneID == b.PlatformSceneID
}

func (o *runtimeStatusOwner) remoteHistory(ctx context.Context, id string, scene botruntime.Scene, observed *recovery.Gate, mediaEnabled bool) (botruntime.BrowserHistory, error) {
	// Source prefers the registered transport's display metadata. The persisted
	// scene still owns the binding, even when runtime registration is older.
	if registered, ok := o.runtime.RegisteredScene(scene.ID); ok && sameHistoryScene(registered, scene) {
		scene = registered
	}
	page, err := o.runtime.FetchRemoteHistory(ctx, scene)
	if err != nil {
		return botruntime.BrowserHistory{}, err
	}
	// An ordinary recall can advance the gate while the adapter is pending.
	// Fence the fresh display read from here, never from before the network wait.
	err = o.publisher.PublishWithGate(ctx, func(q *sql.Conn, gate recovery.Gate) ([]byte, error) {
		current, err := browserScene(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if !sameHistoryScene(current, scene) || gate.Head.InstanceID != observed.Head.InstanceID || gate.Authority != observed.Authority {
			return nil, errors.New("history_scene_binding_changed")
		}
		*observed = gate
		return nil, nil
	}, func([]byte) error { return nil })
	if err != nil {
		return botruntime.BrowserHistory{}, err
	}
	return o.runtime.BrowserRemoteHistory(scene, page, mediaEnabled)
}

func browserHistoryBody(page botruntime.BrowserHistory) ([]byte, error) {
	rows := page.Items
	items := make([]any, 0, len(rows))
	for _, row := range rows {
		presentSceneMessage(row, page.ImageLinks[row])
		// Strict source Message.parse only exposes listed fields.
		message := new(jsjson.Object)
		for _, k := range []string{"id", "sceneId", "senderId", "senderDisplayName", "senderAvatarUrl", "senderPlatformId", "sentAt", "receivedAt", "origin", "replyToId", "status"} {
			if v, ok := row.Get(k); ok {
				_ = message.Set(k, v)
			}
		}
		var parts []any
		if v, ok := row.Get("parts"); ok {
			parts, _ = v.([]any)
		}
		public := make([]any, 0, len(parts))
		for _, v := range parts {
			part, ok := v.(*jsjson.Object)
			if !ok {
				continue
			}
			out := new(jsjson.Object)
			for _, k := range []string{"type", "text", "userId", "mediaType", "description"} {
				if value, exists := part.Get(k); exists {
					_ = out.Set(k, value)
				}
			}
			if kind, _ := part.Get("type"); kind == "media" {
				for _, k := range []string{"mediaKind", "mediaUrl"} {
					if value, exists := part.Get(k); exists {
						_ = out.Set(k, value)
					}
				}
			}
			public = append(public, out)
		}
		_ = message.Set("parts", public)
		items = append(items, message)
	}
	result := new(jsjson.Object)
	_ = result.Set("items", items)
	_ = result.Set("coverage", page.Coverage)
	if page.Reason != nil {
		_ = result.Set("reason", *page.Reason)
	}
	if page.NextCursor != nil {
		_ = result.Set("nextCursor", *page.NextCursor)
	}
	return jsjson.Marshal(result)
}
