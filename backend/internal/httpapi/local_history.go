package httpapi

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func localHistoryBody(runtime *botruntime.Runtime, sceneID, kind string) ([]byte, error) {
	if kind == "" || kind == "simulation" {
		return []byte(`{"items":[],"coverage":"unavailable","reason":"real_scene_required"}`), nil
	}
	rows, err := runtime.LocalHistory(sceneID)
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(rows))
	for _, row := range rows {
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
			typeValue, _ := part.Get("type")
			if typeValue == "unsupported" && platformMetadataKind(part) != "" {
				continue
			}
			out := new(jsjson.Object)
			for _, k := range []string{"type", "text", "userId", "mediaType", "description"} {
				if value, exists := part.Get(k); exists {
					_ = out.Set(k, value)
				}
			}
			// No source reference or dead media link until bytes route lands.
			public = append(public, out)
		}
		_ = message.Set("parts", public)
		items = append(items, message)
	}
	result := new(jsjson.Object)
	_ = result.Set("items", items)
	_ = result.Set("coverage", "partial")
	_ = result.Set("reason", "local_history_window")
	return jsjson.Marshal(result)
}
