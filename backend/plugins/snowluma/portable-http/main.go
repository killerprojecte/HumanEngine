//go:build wasip1

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"runtime"
	"runtime/metrics"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/profileidentity"
	"github.com/killerprojecte/HumanEngine/backend/plugins/abi"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

//go:wasmexport alloc
func alloc(size uint32) uint32 { return abi.Alloc(size) }

//go:wasmexport free
func free(ptr uint32) { abi.Free(ptr) }

//go:wasmexport plugin_call
func call(ptr, size uint32) uint64 {
	sampleGuestHeap(size)
	return abi.Invoke(ptr, size, invoke)
}

func main() {}

var inputSinceHeapSample uint64

func sampleGuestHeap(size uint32) {
	// Sustained inline media can stall automatic collection under the
	// sandbox's synthetic clock. Sample after batches of input, and collect
	// only when the heap has already grown past the automatic GC goal.
	inputSinceHeapSample += uint64(size)
	if inputSinceHeapSample < 1<<20 {
		return
	}
	inputSinceHeapSample = 0
	samples := [...]metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/gc/heap/goal:bytes"}}
	metrics.Read(samples[:])
	if samples[0].Value.Uint64() > 2*samples[1].Value.Uint64() {
		runtime.GC()
	}
}

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
		return map[string]any{"wire": "onebot11-forward-1", "groupNames": true, "privateNames": true, "senderNames": true}
	case "adapterGroupName":
		return sceneName(in, "group", "group_id", "group_name")
	case "adapterPrivateName":
		return sceneName(in, "peer", "user_id", "nickname")
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
	case "adapterHistoryAction":
		return historyAction(in)
	case "adapterHistoryResult":
		return historyResult(in)
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

func sceneName(in *jsjson.Object, expected, field, nameField string) any {
	response := obj(get(in, "response"))
	data := obj(get(response, "data"))
	id, valid := snow.RawID(get(data, field))
	if !valid || id != str(in, expected) || get(response, "status") != "ok" || get(response, "retcode") != float64(0) {
		return nil
	}
	name, present, _ := profileidentity.DisplayName(get(data, nameField))
	if !present {
		return nil
	}
	output, _ := jsjson.NewObject(jsjson.Member{Key: "displayName", Value: name})
	raw, err := jsjson.Marshal(output)
	if err != nil {
		return nil
	}
	return json.RawMessage(raw)
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
	projected := normalizedMessage(adapter, account, event, "live", messageID)
	if projected == nil {
		return nil
	}
	return projected
}

func normalizedMessage(adapter, account string, event *jsjson.Object, origin, messageID string) *jsjson.Object {
	// Canonical message IDs, origin, and authority are owned by the host.
	message, scene, err := snow.Normalize(adapter, account, event, origin, "", false, nil)
	if err != nil {
		return nil
	}
	parts, _ := get(message, "parts").([]any)
	for i, value := range parts {
		part := obj(value)
		switch str(part, "type") {
		case "text", "unsupported":
			continue
		case "mention":
			target, err := snow.PlatformUser(adapter, account, get(part, "userId"))
			if err != nil {
				return nil
			}
			wirePart, _ := jsjson.NewObject(jsjson.Member{Key: "type", Value: "mention"}, jsjson.Member{Key: "platformUserId", Value: target})
			parts[i] = wirePart
			continue
		case "media":
			if str(part, "mediaType") == "image/unknown" {
				continue
			}
		}
		fallback := new(jsjson.Object)
		_ = fallback.Set("type", "unsupported")
		_ = fallback.Set("description", "OneBot part unavailable in the standalone preview")
		parts[i] = fallback
	}
	if len(parts) == 0 {
		ignored, _ := jsjson.NewObject(jsjson.Member{Key: "type", Value: "ignore"})
		return ignored
	}
	peer, _ := jsjson.NewObject(
		jsjson.Member{Key: "kind", Value: str(scene, "kind")},
		jsjson.Member{Key: "id", Value: str(scene, "platformSceneId")},
	)
	out, _ := jsjson.NewObject(
		jsjson.Member{Key: "type", Value: "message"},
		jsjson.Member{Key: "message", Value: messageID},
		jsjson.Member{Key: "peer", Value: peer},
		jsjson.Member{Key: "sender", Value: str(message, "senderPlatformId")},
		jsjson.Member{Key: "parts", Value: parts},
		jsjson.Member{Key: "sentAt", Value: str(message, "sentAt")},
	)
	if name := str(scene, "displayName"); name != "" {
		_ = out.Set("sceneDisplayName", name)
	}
	if name := str(message, "senderDisplayName"); name != "" {
		_ = out.Set("senderDisplayName", name)
	}
	if reply, err := platformReply(str(scene, "id"), str(message, "replyToId")); err == nil && reply != "" {
		_ = out.Set("replyTo", reply)
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
	adapter, account := str(in, "adapter"), str(in, "account")
	var originals []map[string]int
	for i, row := range parts {
		part := obj(row)
		switch str(part, "type") {
		case "text":
			segments = append(segments, segment("text", "text", str(part, "text")))
		case "mention":
			target, err := snow.PlatformUser(adapter, account, get(part, "userId"))
			if err != nil {
				return nil
			}
			segments = append(segments, segment("at", "qq", target))
		case "media":
			originals = append(originals, map[string]int{"part": i, "index": len(segments)})
			segments = append(segments, segment("image", "file", ""))
		default:
			return nil
		}
	}
	out := map[string]any{"action": action, "params": map[string]any{field: str(peer, "id"), "message": segments}}
	if len(originals) != 0 {
		out["originals"] = originals
	}
	return out
}
