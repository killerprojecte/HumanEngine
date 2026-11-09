package snowluma

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// ReceiveDecision preserves OneBotAdapter.receive's protocol branches in the
// guest. The host owns socket identity, echo bookkeeping, clocks, and callbacks;
// it must not infer message/recall routing from raw platform payloads itself.
func ReceiveDecision(input *jsjson.Object) any {
	event, err := object(property(input, "event"))
	if err != nil {
		return receiveFailure(err)
	}
	if echo, ok := property(event, "echo").(string); ok && truthy(property(input, "knownEcho")) {
		if sameString(property(event, "status"), "ok") && property(event, "retcode") == float64(0) {
			result := httpJSON{"kind": "response", "echo": echo}
			if data, exists := event.Get("data"); exists {
				result["data"] = data
			}
			return result
		}
		code := "platform_action_failed"
		if property(event, "retcode") == float64(1404) {
			code = "unsupported"
		} else if property(event, "retcode") == float64(1400) {
			code = "invalid_action_parameters"
		}
		return httpJSON{"kind": "response_error", "echo": echo, "error": code}
	}
	if !truthy(property(input, "ready")) || truthy(property(input, "disposed")) {
		return httpJSON{"kind": "ignore"}
	}
	self := property(event, "self_id")
	if _, missing := self.(missing); missing {
		return httpJSON{"kind": "ignore"}
	}
	account := textProperty(input, "account")
	selfID, err := id(self)
	if err != nil {
		return receiveFailure(err)
	}
	if selfID != account {
		return httpJSON{"kind": "ignore"}
	}
	adapter := textProperty(input, "adapter")
	if sameString(property(event, "post_type"), "message") || sameString(property(event, "post_type"), "message_sent") {
		recalled, _ := property(input, "recalled").(bool)
		message, scene, err := Normalize(adapter, account, event, "live", textProperty(input, "receivedAt"), recalled, nil)
		if err != nil {
			return receiveFailure(err)
		}
		return httpJSON{"kind": "message", "message": message, "scene": scene}
	}
	if !sameString(property(event, "post_type"), "notice") {
		return httpJSON{"kind": "ignore"}
	}
	notice, err := jsString(property(event, "notice_type"))
	if err != nil {
		return receiveFailure(err)
	}
	if notice != "group_recall" && notice != "friend_recall" {
		return httpJSON{"kind": "ignore"}
	}
	kind := "private"
	peerValue := property(event, "user_id")
	if sameString(property(event, "notice_type"), "group_recall") {
		kind = "group"
		peerValue = property(event, "group_id")
	}
	peer, err := id(peerValue)
	if err != nil {
		return receiveFailure(err)
	}
	scene, err := sourceScene(adapter, account, kind, peer)
	if err != nil {
		return receiveFailure(err)
	}
	platform, err := id(property(event, "message_id"))
	if err != nil {
		return receiveFailure(err)
	}
	messageID, err := sourceMessageID(textProperty(scene, "id"), platform)
	if err != nil {
		return receiveFailure(err)
	}
	if !truthy(property(input, "recallCallback")) {
		return httpJSON{"kind": "remember_recall", "rememberRecall": messageID}
	}
	// The source records this ID before evaluating time(event.time). Preserve
	// that mutation even when the recall callback payload cannot be constructed.
	occurredAt, err := sourceTime(property(event, "time"))
	if err != nil {
		result := receiveFailure(err).(httpJSON)
		result["rememberRecall"] = messageID
		return result
	}
	return httpJSON{"kind": "recall", "rememberRecall": messageID, "scene": scene, "recall": makeObject("sceneId", textProperty(scene, "id"), "messageId", messageID, "occurredAt", occurredAt)}
}
func receiveFailure(err error) any {
	_, typed := err.(protocolError)
	return httpJSON{"kind": "error", "error": "invalid_event_or_callback_failed", "cause": err.Error(), "oneBot": typed}
}
