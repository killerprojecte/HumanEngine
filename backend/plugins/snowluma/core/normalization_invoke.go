package snowluma

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

func NormalizationDecision(input *jsjson.Object) any {
	event, err := object(property(input, "event"))
	if err != nil {
		return httpJSON{"error": err.Error(), "oneBot": true}
	}
	adapter := textProperty(input, "adapter")
	account := textProperty(input, "account")
	expected, _ := property(input, "expected").(*jsjson.Object)
	recalled, _ := property(input, "recalled").(bool)
	operation := textProperty(input, "operation")
	if operation == "identify_message" {
		recalled = true
	}
	message, scene, err := Normalize(adapter, account, event, textProperty(input, "origin"), textProperty(input, "receivedAt"), recalled, expected)
	if err != nil {
		_, typed := err.(protocolError)
		return httpJSON{"error": err.Error(), "oneBot": typed}
	}
	if operation == "identify_message" {
		return httpJSON{"messageId": textProperty(message, "id"), "scene": scene}
	}
	return httpJSON{"message": message, "scene": scene}
}
