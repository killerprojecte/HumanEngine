package snowluma

import (
	"encoding/base64"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/profileidentity"
)

type missing struct{}
type protocolError string

func (e protocolError) Error() string { return string(e) }
func property(object *jsjson.Object, key string) any {
	value, ok := object.Get(key)
	if !ok {
		return missing{}
	}
	return value
}
func nullish(value any) bool {
	if value == nil {
		return true
	}
	_, missing := value.(missing)
	return missing
}
func first(values ...any) any {
	for _, value := range values {
		if !nullish(value) {
			return value
		}
	}
	return missing{}
}
func truthy(value any) bool {
	switch value := value.(type) {
	case missing:
		return false
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case float64:
		return value != 0 && !math.IsNaN(value)
	}
	return true
}
func sameString(value any, want string) bool {
	actual, ok := value.(string)
	return ok && actual == want
}
func object(value any) (*jsjson.Object, error) {
	o, ok := value.(*jsjson.Object)
	if !ok || o == nil {
		return nil, protocolError("invalid_payload")
	}
	return o, nil
}
func makeObject(pairs ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for index := 0; index < len(pairs); index += 2 {
		_ = o.Set(pairs[index].(string), pairs[index+1])
	}
	return o
}
func jsString(value any) (string, error) {
	if _, ok := value.(missing); ok {
		return "undefined", nil
	}
	if number, ok := value.(float64); ok {
		if math.IsNaN(number) {
			return "NaN", nil
		}
		if math.IsInf(number, 1) {
			return "Infinity", nil
		}
		if math.IsInf(number, -1) {
			return "-Infinity", nil
		}
	}
	return profileidentity.StringValue(value)
}
func id(value any) (string, error) {
	valueID, ok := RawID(value)
	if !ok {
		return "", protocolError("invalid_identifier")
	}
	return valueID, nil
}
func encodeValue(value any) (string, error) {
	raw, err := jsjson.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func sourceMessageID(scene, platform string) (string, error) {
	encoded, err := encodeValue(platform)
	return scene + ":m:" + encoded, err
}
func sourceUserID(adapter, account, platform string) (string, error) {
	encoded, err := encodeValue([]any{adapter, account, platform})
	return "ob:u:" + encoded, err
}
func sourceTime(value any) (string, error) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Abs(number) > 8640000000000 {
		return "", protocolError("invalid_timestamp")
	}
	stamp := time.UnixMilli(int64(math.Trunc(number * 1000))).UTC()
	year := stamp.Year()
	prefix := fmt.Sprintf("%04d", year)
	if year < 0 {
		prefix = fmt.Sprintf("-%06d", -year)
	} else if year > 9999 {
		prefix = fmt.Sprintf("+%06d", year)
	}
	return prefix + stamp.Format("-01-02T15:04:05.000Z"), nil
}
func sourceScene(adapter, account string, kind any, peer string) (*jsjson.Object, error) {
	identifier, err := profileidentity.OneBotID(adapter, account, kind, peer)
	if err != nil {
		return nil, err
	}
	return makeObject("id", identifier, "kind", kind, "adapterId", adapter, "accountId", account, "platformSceneId", peer), nil
}
func textProperty(o *jsjson.Object, key string) string {
	value, _ := property(o, key).(string)
	return value
}
func optionalName(o *jsjson.Object, key string, value any) {
	name, ok, _ := profileidentity.DisplayName(value)
	if ok {
		_ = o.Set(key, name)
	}
}
func optionalAvatar(o *jsjson.Object, key, id string, group bool) {
	if avatar, ok := profileidentity.QQAvatar(id, group); ok {
		_ = o.Set(key, avatar)
	}
}
func sliceUnits(text string, limit int) string {
	units, _ := jsjson.UTF16(text)
	if len(units) > limit {
		units = units[:limit]
	}
	return jsjson.FromUTF16(units)
}

// Normalize executes the source business projection inside the actual guest.
// Clock and recall facts are supplied by the owning native facilities.
func Normalize(adapter, account string, event *jsjson.Object, origin, receivedAt string, recalled bool, expected *jsjson.Object) (*jsjson.Object, *jsjson.Object, error) {
	if value := property(event, "self_id"); !isMissing(value) {
		self, err := id(value)
		if err != nil {
			return nil, nil, err
		}
		if self != account {
			return nil, nil, protocolError("account_mismatch")
		}
	}
	kind := property(event, "message_type")
	kindName, err := jsString(kind)
	if err != nil {
		return nil, nil, err
	}
	if kindName != "group" && kindName != "private" {
		return nil, nil, protocolError("unsupported_scene_type")
	}
	if sameString(kind, "private") && sameString(property(event, "sub_type"), "group") {
		return nil, nil, protocolError("temporary_private_session_unsupported")
	}
	senderRaw := property(event, "user_id")
	if nullish(senderRaw) {
		senderObject, err := object(property(event, "sender"))
		if err != nil {
			return nil, nil, err
		}
		senderRaw = property(senderObject, "user_id")
	}
	sender, err := id(senderRaw)
	if err != nil {
		return nil, nil, err
	}
	var peer string
	if sameString(kind, "group") {
		peer, err = id(property(event, "group_id"))
	} else if sender == account {
		peer, err = id(property(event, "target_id"))
	} else {
		peer = sender
	}
	if err != nil {
		return nil, nil, err
	}
	scene, err := sourceScene(adapter, account, kind, peer)
	if err != nil {
		return nil, nil, err
	}
	sceneID := textProperty(scene, "id")
	if expected != nil && sceneID != textProperty(expected, "id") {
		return nil, nil, protocolError("scene_mismatch")
	}
	senderInfo, ok := property(event, "sender").(*jsjson.Object)
	if !ok || senderInfo == nil {
		senderInfo = new(jsjson.Object)
	}
	display, hasDisplay, _ := profileidentity.DisplayName(property(senderInfo, "card"))
	if !hasDisplay {
		display, hasDisplay, _ = profileidentity.DisplayName(property(senderInfo, "nickname"))
	}
	if sameString(kind, "group") {
		optionalName(scene, "displayName", property(event, "group_name"))
	} else if sender == peer && hasDisplay {
		_ = scene.Set("displayName", display)
	}
	optionalAvatar(scene, "avatarUrl", peer, sameString(kind, "group"))
	platformMessage, err := id(property(event, "message_id"))
	if err != nil {
		return nil, nil, err
	}
	// OneBotAdapter.messageId validates the scene after rawId(message_id).
	// String coercion above does not make an array a valid scene kind.
	if !sameString(kind, "group") && !sameString(kind, "private") {
		return nil, nil, protocolError("scene_scope_mismatch")
	}
	messageID, err := sourceMessageID(sceneID, platformMessage)
	if err != nil {
		return nil, nil, err
	}
	userID, err := sourceUserID(adapter, account, sender)
	if err != nil {
		return nil, nil, err
	}
	sentAt, err := sourceTime(property(event, "time"))
	if err != nil {
		return nil, nil, err
	}
	message := makeObject("id", messageID, "sceneId", sceneID, "senderId", userID, "senderPlatformId", sender)
	if hasDisplay {
		_ = message.Set("senderDisplayName", display)
	}
	optionalAvatar(message, "senderAvatarUrl", sender, false)
	_ = message.Set("sentAt", sentAt)
	_ = message.Set("receivedAt", receivedAt)
	_ = message.Set("origin", origin)
	parts := []any{}
	_ = message.Set("parts", parts)
	_ = message.Set("status", "available")
	if recalled {
		_ = message.Set("status", "recalled")
		return message, scene, nil
	}
	segments := property(event, "message")
	if text, ok := segments.(string); ok {
		parsed := ParseCQ(text)
		values := make([]any, 0, len(parsed))
		for _, segment := range parsed {
			data := new(jsjson.Object)
			for key, value := range segment.Data {
				_ = data.Set(key, value)
			}
			values = append(values, makeObject("type", segment.Type, "data", data))
		}
		segments = values
	}
	rows, ok := segments.([]any)
	if !ok {
		_ = message.Set("parts", []any{makeObject("type", "unsupported", "description", "Configure OneBot array message format")})
		return message, scene, nil
	}
	for _, raw := range rows {
		segment, err := object(raw)
		if err != nil {
			return nil, nil, err
		}
		data, err := object(property(segment, "data"))
		if err != nil {
			return nil, nil, err
		}
		segmentType := property(segment, "type")
		typeName, err := jsString(segmentType)
		if err != nil {
			return nil, nil, err
		}
		if sameString(segmentType, "text") {
			if text, ok := property(data, "text").(string); ok {
				parts = append(parts, makeObject("type", "text", "text", text))
				continue
			}
		}
		switch {
		case sameString(segmentType, "at"):
			target := "all"
			if !sameString(property(data, "qq"), "all") {
				platform, err := id(property(data, "qq"))
				if err != nil {
					return nil, nil, err
				}
				target, err = sourceUserID(adapter, account, platform)
				if err != nil {
					return nil, nil, err
				}
			}
			parts = append(parts, makeObject("type", "mention", "userId", target))
		case sameString(segmentType, "reply"):
			platform, err := id(property(data, "id"))
			if err != nil {
				return nil, nil, err
			}
			reply, err := sourceMessageID(sceneID, platform)
			if err != nil {
				return nil, nil, err
			}
			_ = message.Set("replyToId", reply)
		case typeName == "image" || typeName == "mface" || typeName == "sticker" || typeName == "emoji":
			image, err := imageParts(sceneID, typeName, data)
			if err != nil {
				return nil, nil, err
			}
			parts = append(parts, image...)
		case sameString(segmentType, "face"):
			part, err := facePart(data)
			if err != nil {
				return nil, nil, err
			}
			parts = append(parts, part)
		case typeName == "record" || typeName == "video":
			source := firstTruthy(property(data, "url"), property(data, "file"), property(data, "file_id"))
			if !truthy(source) {
				if text, ok := property(data, "base64").(string); ok {
					source = "base64://" + text
				}
			}
			file, err := mediaReference(source)
			if err != nil {
				return nil, nil, err
			}
			descriptor := makeObject("sceneId", sceneID, "type", segmentType, "file", file)
			if value, ok := first(property(data, "file_id"), property(data, "file")).(string); ok {
				_ = descriptor.Set("fileId", value)
			}
			encoded, err := encodeValue(descriptor)
			if err != nil {
				return nil, nil, err
			}
			mediaType := "video/unknown"
			if sameString(segmentType, "record") {
				mediaType = "audio/unknown"
			}
			parts = append(parts, makeObject("type", "media", "assetId", "ob:media:"+encoded, "mediaType", mediaType))
		default:
			parts = append(parts, makeObject("type", "unsupported", "description", "OneBot segment: "+sliceUnits(typeName, 40)))
		}
	}
	_ = message.Set("parts", parts)
	return message, scene, nil
}
func isMissing(value any) bool { _, ok := value.(missing); return ok }
func firstTruthy(values ...any) any {
	for _, value := range values {
		if truthy(value) {
			return value
		}
	}
	return missing{}
}
func mediaReference(value any) (string, error) {
	text, ok := value.(string)
	if !ok || text == "" {
		return "", protocolError("invalid_media_reference")
	}
	units, err := jsjson.UTF16(text)
	if err != nil {
		return "", err
	}
	limit := 16384
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "base64://") || strings.HasPrefix(lower, "data:image/") {
		limit = (64*1024*1024+2)/3*4 + 64
	}
	if len(units) > limit {
		return "", protocolError("invalid_media_reference")
	}
	return text, nil
}
func label(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	units, _ := jsjson.UTF16(text)
	kept := units[:0]
	for _, unit := range units {
		if unit > 31 && unit != 127 {
			kept = append(kept, unit)
		}
	}
	start, end := 0, len(kept)
	for start < end && jsSpace(kept[start]) {
		start++
	}
	for end > start && jsSpace(kept[end-1]) {
		end--
	}
	kept = kept[start:end]
	if len(kept) > 200 {
		kept = kept[:200]
	}
	return jsjson.FromUTF16(kept)
}
func jsSpace(unit uint16) bool {
	return unit >= 9 && unit <= 13 || unit == 0x20 || unit == 0xa0 || unit == 0x1680 || unit >= 0x2000 && unit <= 0x200a || unit == 0x2028 || unit == 0x2029 || unit == 0x202f || unit == 0x205f || unit == 0x3000 || unit == 0xfeff
}
func imageParts(sceneID, kind string, data *jsjson.Object) ([]any, error) {
	parts := []any{}
	var inline, inlineFile any = missing{}, missing{}
	if text, ok := property(data, "base64").(string); ok && text != "" {
		inline = "base64://" + text
	}
	if text, ok := property(data, "file").(string); ok {
		lower := strings.ToLower(text)
		if strings.HasPrefix(lower, "base64://") || strings.HasPrefix(lower, "data:image/") {
			inlineFile = text
		}
	}
	source := firstTruthy(inline, inlineFile, property(data, "url"), property(data, "file"), property(data, "file_id"))
	file, err := mediaReference(source)
	if err != nil {
		parts = append(parts, makeObject("type", "unsupported", "description", "Image unavailable: "+kind+" has no usable media reference"))
	} else {
		descriptor := makeObject("sceneId", sceneID, "type", "image", "file", file)
		if fileID, ok := first(property(data, "file_id"), property(data, "file")).(string); ok && fileID != "" && fileID != file {
			_ = descriptor.Set("fileId", fileID)
		}
		encoded, err := encodeValue(descriptor)
		if err != nil {
			return nil, err
		}
		parts = append(parts, makeObject("type", "media", "mediaType", "image/unknown", "assetId", "ob:media:"+encoded))
	}
	summary := label(first(property(data, "summary"), property(data, "name")))
	subtype := ""
	switch value := property(data, "sub_type").(type) {
	case string:
		subtype = label(value)
	case float64:
		text, err := jsString(value)
		if err != nil {
			return nil, err
		}
		subtype = label(text)
	}
	emoji := truthy(property(data, "emoji_id")) || truthy(property(data, "emoji_package_id"))
	if kind != "image" || summary != "" || subtype != "" || emoji {
		actualKind := kind
		if kind == "image" && emoji {
			actualKind = "mface"
		}
		metadata := makeObject("kind", actualKind)
		if summary != "" {
			_ = metadata.Set("summary", summary)
		}
		if subtype != "" {
			_ = metadata.Set("subtype", subtype)
		}
		text, err := jsjson.Stringify(metadata)
		if err != nil {
			return nil, err
		}
		parts = append(parts, makeObject("type", "unsupported", "description", "Untrusted platform image metadata: "+text))
	}
	return parts, nil
}
func facePart(data *jsjson.Object) (*jsjson.Object, error) {
	name := label(first(property(data, "text"), property(data, "name"), property(data, "summary")))
	value := first(property(data, "id"), "")
	identifier, err := jsString(value)
	if err != nil {
		return nil, err
	}
	valid := len(identifier) > 0 && len(identifier) <= 8
	for _, character := range identifier {
		if character < '0' || character > '9' {
			valid = false
		}
	}
	metadata := new(jsjson.Object)
	if valid {
		_ = metadata.Set("id", identifier)
	}
	if name != "" {
		_ = metadata.Set("label", name)
	} else {
		_ = metadata.Set("meaning", "unavailable")
	}
	text, err := jsjson.Stringify(metadata)
	if err != nil {
		return nil, err
	}
	return makeObject("type", "unsupported", "description", "Native QQ face metadata (untrusted): "+text), nil
}
