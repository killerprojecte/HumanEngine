package httpapi

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func sceneMessagesBody(runtime *botruntime.Runtime, sceneID string, mediaEnabled bool) ([]byte, error) {
	rows, links, err := runtime.PreviewMessages(sceneID, mediaEnabled)
	if err != nil {
		return nil, err
	}
	messages := make([]any, len(rows))
	for i, row := range rows {
		presentSceneMessage(row, links[i])
		messages[i] = row
	}
	return jsjson.Marshal(messages)
}

func presentSceneMessage(row *jsjson.Object, links botruntime.DisplayImageLinks) {
	status, _ := row.Get("status")
	origin, _ := row.Get("origin")
	available := status == "available" && origin != "simulation"
	value, _ := row.Get("parts")
	parts, _ := value.([]any)
	// The display projector owns this copy; compact without reallocating.
	public := parts[:0]
	var mediaBase string
	var unavailableBase bool
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
			// URLs on the private row are not display authority.
			part.Delete("mediaUrl")
			part.Delete("mediaKind")
			mediaType, _ := part.Get("mediaType")
			mime, _ := mediaType.(string)
			if available && (mime == "image" || len(mime) >= len("image/") && strings.EqualFold(mime[:len("image/")], "image/")) {
				linked := links.InlineAt(i)
				if !linked && links.Managed && i <= 255 {
					value, _ := part.Get("assetId")
					asset, _ := value.(string)
					linked = botruntime.IsManagedImageAssetID(asset)
				}
				if linked && !unavailableBase {
					if mediaBase == "" {
						mediaBase = sceneMediaBase(row)
						unavailableBase = mediaBase == ""
					}
					if mediaBase != "" {
						_ = part.Set("mediaUrl", mediaBase+strconv.Itoa(i))
					}
				}
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
				_ = part.Set("mediaKind", mediaKind)
			}
			part.Delete("assetId")
		}
		public = append(public, part)
	}
	_ = row.Set("parts", public)
}

func sceneMediaBase(row *jsjson.Object) string {
	sceneValue, _ := row.Get("sceneId")
	scene, _ := sceneValue.(string)
	idValue, _ := row.Get("id")
	id, _ := idValue.(string)
	if !sceneMediaLinkSegment(scene, 4096) || !sceneMediaLinkSegment(id, 8192) {
		return ""
	}
	return "/api/scenes/" + sceneMediaComponent(scene) + "/messages/" + sceneMediaComponent(id) + "/media/"
}

func sceneMediaLinkSegment(s string, max int) bool {
	return s != "" && s != "." && s != ".." && utf8.ValidString(s) &&
		(len(s) <= max || utf8.RuneCountInString(s) <= max)
}

func sceneMediaComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := range len(s) {
		c := s[i]
		safe := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("-_.!~*'()", c) >= 0
		if safe {
			if encoded.Cap() != 0 {
				encoded.WriteByte(c)
			}
		} else {
			if encoded.Cap() == 0 {
				encoded.Grow(len(s) + 2)
				encoded.WriteString(s[:i])
			}
			encoded.WriteByte('%')
			encoded.WriteByte(hex[c>>4])
			encoded.WriteByte(hex[c&15])
		}
	}
	if encoded.Cap() == 0 {
		return s
	}
	return encoded.String()
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
