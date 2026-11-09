package snowluma

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"golang.org/x/text/encoding/unicode"
)

// SendDecision performs protocol work in the guest. A media pause asks the host
// for exactly one native resolver result in source order; it is not permission
// to preload arbitrary files or resolve later parts after a prior failure.
func SendDecision(input *jsjson.Object) any {
	operation := textProperty(input, "operation")
	if operation == "send_validate" {
		scene, err := object(property(input, "scene"))
		if err == nil {
			err = assertSourceScene(textProperty(input, "adapter"), textProperty(input, "account"), scene)
		}
		if err != nil {
			code := "send_failed"
			if typed, ok := err.(protocolError); ok {
				code = string(typed)
			}
			return httpJSON{"kind": "invalid", "code": code}
		}
		return httpJSON{"kind": "valid"}
	}
	if operation == "send_result" {
		return sendResult(input)
	}
	aborted, _ := property(input, "aborted").(bool)
	if operation == "send_begin" && aborted {
		return httpJSON{"kind": "receipt", "receipt": makeObject("status", "cancelled", "detail", "cancelled_before_send")}
	}
	var state *jsjson.Object
	var err error
	if operation == "send_begin" {
		scene, ok := property(input, "scene").(*jsjson.Object)
		if !ok {
			return sendFailure(errors.New("invalid scene"), false, aborted)
		}
		adapter, account := textProperty(input, "adapter"), textProperty(input, "account")
		if err = assertSourceScene(adapter, account, scene); err != nil {
			return sendFailure(err, false, aborted)
		}
		segment, ok := property(input, "segment").(*jsjson.Object)
		if !ok {
			return sendFailure(errors.New("invalid segment"), false, aborted)
		}
		parts, ok := property(segment, "parts").([]any)
		if !ok {
			return sendFailure(errors.New("invalid parts"), false, aborted)
		}
		message := []any{}
		if reply := property(segment, "replyToId"); truthy(reply) {
			platform, err := platformMessage(scene, reply)
			if err != nil {
				return sendFailure(err, false, aborted)
			}
			message = append(message, makeObject("type", "reply", "data", makeObject("id", platform)))
		}
		state = makeObject("scene", scene, "adapter", adapter, "account", account, "parts", parts, "at", float64(0), "message", message, "sources", []any{})
	} else {
		state, err = object(property(input, "state"))
		if err != nil {
			return sendFailure(err, false, aborted)
		}
		parts, _ := property(state, "parts").([]any)
		at, _ := property(state, "at").(float64)
		if at < 0 || int(at) >= len(parts) {
			return sendFailure(errors.New("invalid send continuation"), false, aborted)
		}
		part, _ := parts[int(at)].(*jsjson.Object)
		if part == nil {
			return sendFailure(errors.New("invalid part"), false, aborted)
		}
		message, _ := property(state, "message").([]any)
		sources, _ := property(state, "sources").([]any)
		source := property(input, "source")
		if operation == "send_source" && truthy(source) {
			source, err := object(source)
			if err != nil {
				return sendFailure(err, false, aborted)
			}
			kind := property(source, "type")
			if !sameString(kind, "image") && !sameString(kind, "record") && !sameString(kind, "video") {
				return sendFailure(protocolError("media_scope_or_type_unsupported"), false, aborted)
			}
			sources = append(sources, makeObject("index", float64(len(message)), "path", property(source, "path"), "bytes", property(source, "bytes"), "hash", property(source, "hash")))
			message = append(message, makeObject("type", kind, "data", makeObject("file", "")))
		} else if operation == "send_source" {
			return httpJSON{"kind": "resolve_media", "state": state, "assetId": property(part, "assetId"), "scene": property(state, "scene")}
		} else if operation == "send_media" {
			scene, _ := property(state, "scene").(*jsjson.Object)
			var media *jsjson.Object
			if resolved := property(input, "media"); truthy(resolved) {
				media, err = object(resolved)
				if err == nil {
					media = media.Clone()
					_ = media.Set("sceneId", property(scene, "id"))
				}
			} else {
				asset, ok := property(part, "assetId").(string)
				if !ok {
					return sendFailure(errors.New("invalid asset id"), false, aborted)
				}
				encoded := ""
				if strings.HasPrefix(asset, "ob:media:") {
					encoded = asset[9:]
				}
				decoded, e := decodeSourceID(encoded)
				err = e
				if err == nil {
					media, err = object(decoded)
				}
			}
			if err != nil {
				return sendFailure(err, false, aborted)
			}
			kind, err := jsString(property(media, "type"))
			if err != nil {
				return sendFailure(err, false, aborted)
			}
			if property(media, "sceneId") != property(scene, "id") || (kind != "image" && kind != "record" && kind != "video") {
				return sendFailure(protocolError("media_scope_or_type_unsupported"), false, aborted)
			}
			file, err := mediaReference(property(media, "file"))
			if err != nil {
				return sendFailure(err, false, aborted)
			}
			message = append(message, makeObject("type", property(media, "type"), "data", makeObject("file", file)))
		} else {
			return sendFailure(errors.New("invalid send operation"), false, aborted)
		}
		_ = state.Set("message", message)
		_ = state.Set("sources", sources)
		_ = state.Set("at", at+1)
	}
	return continueSend(state, aborted)
}
func continueSend(state *jsjson.Object, aborted bool) any {
	parts, _ := property(state, "parts").([]any)
	at, _ := property(state, "at").(float64)
	message, _ := property(state, "message").([]any)
	scene, _ := property(state, "scene").(*jsjson.Object)
	for int(at) < len(parts) {
		part, ok := parts[int(at)].(*jsjson.Object)
		if !ok {
			return sendFailure(errors.New("invalid part"), false, aborted)
		}
		switch {
		case sameString(property(part, "type"), "text"):
			data := new(jsjson.Object)
			if text, exists := part.Get("text"); exists {
				_ = data.Set("text", text)
			}
			message = append(message, makeObject("type", "text", "data", data))
		case sameString(property(part, "type"), "mention"):
			user, err := platformUser(textProperty(state, "adapter"), textProperty(state, "account"), property(part, "userId"))
			if err != nil {
				return sendFailure(err, false, aborted)
			}
			message = append(message, makeObject("type", "at", "data", makeObject("qq", user)))
		case sameString(property(part, "type"), "media"):
			_ = state.Set("at", at)
			_ = state.Set("message", message)
			return httpJSON{"kind": "resolve_source", "state": state, "assetId": property(part, "assetId"), "scene": scene}
		default:
			return sendFailure(protocolError("unsupported_message_part"), false, aborted)
		}
		at++
	}
	if len(message) == 0 {
		return sendFailure(protocolError("empty_message"), false, aborted)
	}
	name, key := "send_private_msg", "user_id"
	if sameString(property(scene, "kind"), "group") {
		name, key = "send_group_msg", "group_id"
	}
	return httpJSON{"kind": "action", "state": state, "action": name, "params": makeObject(key, property(scene, "platformSceneId"), "message", message), "sources": property(state, "sources")}
}
func assertSourceScene(adapter, account string, scene *jsjson.Object) error {
	kind := property(scene, "kind")
	encoded, err := encodeValue([]any{adapter, account, kind, property(scene, "platformSceneId")})
	expected := "ob:s:" + encoded
	if err != nil {
		return err
	}
	if (!sameString(kind, "group") && !sameString(kind, "private")) || !sameString(property(scene, "id"), expected) || !sameString(property(scene, "adapterId"), adapter) || !sameString(property(scene, "accountId"), account) {
		return protocolError("scene_scope_mismatch")
	}
	return nil
}
func platformMessage(scene *jsjson.Object, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", errors.New("invalid message identifier")
	}
	prefix := textProperty(scene, "id") + ":m:"
	if !strings.HasPrefix(text, prefix) {
		return "", protocolError("message_scope_mismatch")
	}
	value, err := decodeSourceID(text[len(prefix):])
	if err != nil {
		return "", err
	}
	return id(value)
}
func platformUser(adapter, account string, value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", errors.New("invalid user identifier")
	}
	if text == "all" {
		return text, nil
	}
	encoded := ""
	if strings.HasPrefix(text, "ob:u:") {
		encoded = text[5:]
	}
	value, err := decodeSourceID(encoded)
	if err != nil {
		return "", err
	}
	items, ok := value.([]any)
	if !ok || len(items) < 2 || !sameString(items[0], adapter) || !sameString(items[1], account) {
		return "", protocolError("user_scope_mismatch")
	}
	if len(items) < 3 {
		return "", protocolError("invalid_identifier")
	}
	return id(items[2])
}
func decodeSourceID(value string) (any, error) {
	units, err := jsjson.UTF16(value)
	if err != nil {
		return nil, protocolError("invalid_identifier")
	}
	data := []byte{}
	for _, u := range units {
		b := byte(u)
		if b == '=' {
			break
		}
		if b == '-' {
			b = '+'
		} else if b == '_' {
			b = '/'
		}
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '+' || b == '/' {
			data = append(data, b)
		}
	}
	if len(data)%4 == 1 {
		data = data[:len(data)-1]
	}
	raw, err := base64.RawStdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, protocolError("invalid_identifier")
	}
	text, err := unicode.UTF8.NewDecoder().Bytes(raw)
	if err != nil {
		return nil, protocolError("invalid_identifier")
	}
	parsed, err := jsjson.ParseBytes(text)
	if err != nil {
		return nil, protocolError("invalid_identifier")
	}
	return parsed, nil
}
func sendFailure(err error, acknowledged, aborted bool) any {
	code := "send_failed"
	if typed, ok := err.(protocolError); ok {
		code = string(typed)
	}
	status := "failed"
	if acknowledged {
		status = "unknown"
	} else if aborted {
		status = "cancelled"
	}
	return httpJSON{"kind": "receipt", "receipt": makeObject("status", status, "detail", code), "failure": makeObject("code", code, "transmitted", acknowledged)}
}
func sendResult(input *jsjson.Object) any {
	aborted, _ := property(input, "aborted").(bool)
	if failure, ok := property(input, "actionError").(*jsjson.Object); ok {
		transmitted, _ := property(failure, "transmitted").(bool)
		return sendFailure(protocolError(textProperty(failure, "code")), transmitted, aborted)
	}
	result, err := object(property(input, "response"))
	if err != nil {
		outcome := sendFailure(err, false, aborted).(httpJSON)
		_ = outcome["receipt"].(*jsjson.Object).Set("status", "unknown")
		return outcome
	}
	scene, _ := property(input, "scene").(*jsjson.Object)
	platform, err := id(property(result, "message_id"))
	if err == nil {
		err = assertSourceScene(textProperty(input, "adapter"), textProperty(input, "account"), scene)
	}
	if err != nil {
		return sendFailure(protocolError("missing_send_receipt"), true, aborted)
	}
	messageID, err := sourceMessageID(textProperty(scene, "id"), platform)
	if err != nil {
		return sendFailure(protocolError("missing_send_receipt"), true, aborted)
	}
	return httpJSON{"kind": "receipt", "receipt": makeObject("status", "sent", "messageId", messageID)}
}
