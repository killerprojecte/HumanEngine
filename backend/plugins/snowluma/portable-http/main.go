//go:build wasip1

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/plugins/abi"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

//go:wasmexport alloc
func alloc(size uint32) uint32 { return abi.Alloc(size) }

//go:wasmexport free
func free(ptr uint32) { abi.Free(ptr) }

//go:wasmexport plugin_call
func call(ptr, size uint32) uint64 { return abi.Invoke(ptr, size, invoke) }
func main()                        {}

func get(o *jsjson.Object, key string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(key)
	return v
}
func obj(v any) *jsjson.Object                { o, _ := v.(*jsjson.Object); return o }
func str(o *jsjson.Object, key string) string { v, _ := get(o, key).(string); return v }

func invoke(raw []byte) any {
	v, err := jsjson.ParseBytes(raw)
	if err != nil {
		return nil
	}
	q := obj(v)
	in := obj(get(q, "input"))
	switch str(q, "operation") {
	case "adapterProtocol":
		return map[string]string{"wire": "onebot11-http-2"}
	case "adapterLogin":
		account := str(in, "account")
		response := obj(get(in, "response"))
		data := obj(get(response, "data"))
		bot, valid := snow.RawID(get(data, "user_id"))
		if valid && bot == account && get(response, "status") == "ok" && get(response, "retcode") == float64(0) {
			return map[string]string{"account": account}
		}
	case "adapterIngress":
		return ingress(str(in, "adapter"), str(in, "account"), obj(get(in, "event")))
	case "adapterImage":
		if get(in, "status") == "ok" && get(in, "retcode") == float64(0) {
			data := obj(get(in, "data"))
			source := str(data, "url")
			if source == "" {
				source = str(data, "file")
			}
			return map[string]string{"source": source}
		}
	case "adapterSend":
		return send(in)
	case "adapterReceipt":
		if get(in, "status") == "ok" && get(in, "retcode") == float64(0) {
			if message, ok := snow.RawID(get(obj(get(in, "data")), "message_id")); ok {
				return map[string]string{"message": message}
			}
		}
	}
	return nil
}

func ingress(adapter, account string, event *jsjson.Object) any {
	bot, valid := snow.RawID(get(event, "self_id"))
	if !valid || bot != account {
		return nil
	}
	typ := str(event, "post_type")
	messageID, valid := snow.RawID(get(event, "message_id"))
	if typ == "notice" {
		kind, peerKey := "", ""
		switch str(event, "notice_type") {
		case "group_recall":
			kind, peerKey = "group", "group_id"
		case "friend_recall":
			kind, peerKey = "private", "user_id"
		default:
			return map[string]string{"type": "ignore"}
		}
		peer, ok := snow.RawID(get(event, peerKey))
		if !ok || !valid {
			return nil
		}
		return map[string]any{"type": "recall", "peer": map[string]string{"kind": kind, "id": peer}, "message": messageID}
	}
	if typ != "message" && typ != "message_sent" {
		return map[string]string{"type": "ignore"}
	}
	if str(event, "message_type") == "private" && str(event, "sub_type") == "group" {
		return map[string]string{"type": "ignore"}
	}
	if !valid {
		return nil
	}
	// Only the peer and normalized business fields cross the ABI. The host
	// derives canonical IDs and stamps the actual receive time.
	message, scene, err := snow.Normalize(adapter, account, event, "live", "", false, nil)
	if err != nil {
		return nil
	}
	parts, _ := get(message, "parts").([]any)
	for i, value := range parts {
		part := obj(value)
		switch str(part, "type") {
		case "text", "unsupported":
			continue
		case "media":
			if str(part, "mediaType") == "image/unknown" {
				continue
			}
		}
		fallback := new(jsjson.Object)
		_ = fallback.Set("type", "unsupported")
		_ = fallback.Set("description", "OneBot part unavailable in the HTTP preview")
		parts[i] = fallback
	}
	if len(parts) == 0 {
		return map[string]string{"type": "ignore"}
	}
	wire, err := jsjson.Marshal(parts)
	if err != nil {
		return nil
	}
	out := map[string]any{"type": "message", "message": messageID,
		"peer":   map[string]string{"kind": str(scene, "kind"), "id": str(scene, "platformSceneId")},
		"sender": str(message, "senderPlatformId"), "parts": json.RawMessage(wire), "sentAt": str(message, "sentAt")}
	if reply, err := platformReply(str(scene, "id"), str(message, "replyToId")); err == nil && reply != "" {
		out["replyTo"] = reply
	}
	return out
}
func platformReply(scene, reply string) (string, error) {
	if reply == "" {
		return "", nil
	}
	value, ok := strings.CutPrefix(reply, scene+":m:")
	if !ok {
		return "", errors.New("reply_scope")
	}
	bytes, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	decoded, err := jsjson.ParseBytes(bytes)
	if err != nil {
		return "", err
	}
	id, _ := decoded.(string)
	return id, nil
}
func send(in *jsjson.Object) any {
	peer := obj(get(in, "peer"))
	action, field := "send_group_msg", "group_id"
	if str(peer, "kind") == "private" {
		action, field = "send_private_msg", "user_id"
	}
	parts, _ := get(in, "parts").([]any)
	segments := make([]any, 0, len(parts)+1)
	segment := func(typ, key, val string) map[string]any {
		return map[string]any{"type": typ, "data": map[string]string{key: val}}
	}
	if reply := str(in, "replyTo"); reply != "" {
		segments = append(segments, segment("reply", "id", reply))
	}
	for _, row := range parts {
		part := obj(row)
		if str(part, "type") != "text" {
			return nil
		}
		segments = append(segments, segment("text", "text", str(part, "text")))
	}
	return map[string]any{"action": action, "params": map[string]any{field: str(peer, "id"), "message": segments}}
}
