package httpapi

import (
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func sceneMessagesBody(runtime *botruntime.Runtime, sceneID string) ([]byte, error) {
	rows, err := runtime.PreviewMessages(sceneID)
	if err != nil {
		return nil, err
	}
	messages := make([]any, len(rows))
	for i, row := range rows {
		status, _ := row.Get("status")
		origin, _ := row.Get("origin")
		available := status == "available" && origin != "simulation"
		value, _ := row.Get("parts")
		parts, _ := value.([]any)
		// The display projector owns this copy; compact without reallocating.
		public := parts[:0]
		for i := 0; i < len(parts); i++ {
			value := parts[i]
			part, ok := value.(*jsjson.Object)
			if !ok {
				public = append(public, value)
				continue
			}
			kind, _ := part.Get("type")
			switch kind {
			case "unsupported":
				if platformMetadataKind(part) != "" {
					continue
				}
			case "media":
				part.Delete("assetId")
				mediaType, _ := part.Get("mediaType")
				mime, _ := mediaType.(string)
				if available && (mime == "image" || len(mime) >= len("image/") && strings.EqualFold(mime[:len("image/")], "image/")) {
					mediaKind := "image"
					if i+1 < len(parts) {
						if metadata, ok := parts[i+1].(*jsjson.Object); ok {
							if kind := platformMetadataKind(metadata); kind != "" {
								i++ // Its public representation is the image kind.
								if kind != "image" {
									mediaKind = "sticker"
								}
							}
						}
					}
					// The Go preview does not emit a URL before the byte endpoint owns it.
					_ = part.Set("mediaKind", mediaKind)
				}
			}
			public = append(public, part)
		}
		_ = row.Set("parts", public)
		messages[i] = row
	}
	return jsjson.Marshal(messages)
}

func platformMetadataKind(part *jsjson.Object) string {
	kind, _ := part.Get("type")
	if kind != "unsupported" {
		return ""
	}
	value, _ := part.Get("description")
	text, _ := value.(string)
	raw, ok := strings.CutPrefix(text, "Untrusted platform image metadata: ")
	if !ok {
		return ""
	}
	value, err := jsjson.Parse(raw)
	metadata, ok := value.(*jsjson.Object)
	if err != nil || !ok {
		return ""
	}
	kind, _ = metadata.Get("kind")
	switch kind {
	case "image", "mface", "sticker", "emoji":
		return kind.(string)
	}
	return ""
}
