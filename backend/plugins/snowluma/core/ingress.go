package snowluma

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// IngressDecision adds only the source callback boundaries around the accepted
// receive/normalization implementation. Recalled IDs are opaque guest-owned
// state, retained until this adapter generation is disposed.
func IngressDecision(input *jsjson.Object) any {
	state, _ := object(property(input, "state"))
	if state == nil {
		state = makeObject("recalled", []any{})
	}
	operation := textProperty(input, "operation")
	if operation == "ingress_callback" {
		message, err := object(property(input, "message"))
		if err != nil {
			return receiveFailure(err)
		}
		bot, err := sourceUserID(textProperty(input, "adapter"), textProperty(input, "account"), textProperty(input, "account"))
		if err != nil {
			return receiveFailure(err)
		}
		if sameString(property(message, "senderId"), bot) {
			message = message.Clone()
			_ = message.Set("senderId", "bot")
		}
		return httpJSON{"kind": "ingest", "state": state, "message": message, "scene": property(input, "scene")}
	}
	request := input.Clone()
	_ = request.Set("ready", true)
	_ = request.Set("recallCallback", true)
	if operation == "ingress_begin" {
		_ = request.Set("recalled", true)
	}
	decision := ReceiveDecision(request).(httpJSON)
	if id, exists := decision["rememberRecall"]; exists {
		ids, _ := property(state, "recalled").([]any)
		found := false
		for _, v := range ids {
			if v == id {
				found = true
				break
			}
		}
		if !found {
			_ = state.Set("recalled", append(ids, id))
		}
	}
	decision["state"] = state
	if decision["kind"] != "message" {
		return decision
	}
	message := decision["message"].(*jsjson.Object)
	scene := decision["scene"].(*jsjson.Object)
	if operation == "ingress_begin" {
		id := textProperty(message, "id")
		ids, _ := property(state, "recalled").([]any)
		known := false
		for _, v := range ids {
			if v == id {
				known = true
				break
			}
		}
		if !known {
			return httpJSON{"kind": "is_recalled", "state": state, "messageId": id, "sceneId": textProperty(scene, "id"), "request": request}
		}
	}
	// A remembered event already has the source's recalled empty-parts shape;
	// otherwise ingress_recalled supplies the actual owner result to Normalize.
	decision["kind"] = "observe_profiles"
	return decision
}

func RecalledLookupDecision(input *jsjson.Object) any {
	state, _ := object(property(input, "state"))
	if state != nil {
		ids, _ := property(state, "recalled").([]any)
		for _, v := range ids {
			if v == property(input, "messageId") {
				return httpJSON{"kind": "recalled", "value": true}
			}
		}
	}
	return httpJSON{"kind": "is_recalled", "sceneId": property(input, "sceneId"), "messageId": property(input, "messageId")}
}
