package botruntime

import "testing"

func TestPluginEventRecallAtDelivery(t *testing.T) {
	a := newTestAuthority()
	r := &Runtime{services: Services{Authority: a}}
	for _, kind := range []string{"message.received", "message.sent"} {
		t.Run(kind, func(t *testing.T) {
			a.recalled["s|m"] = false
			event := obj("type", kind, "scene", obj("id", "s"), "message", obj("id", "m", "status", "available", "parts", []any{"private"}), "result", obj("messageId", "m"), "segment", obj("parts", []any{"private"}))
			first, err := r.PreparePluginEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			a.recalled["s|m"] = true
			second, err := r.PreparePluginEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			field := "message"
			if kind == "message.sent" {
				field = "segment"
			}
			if len(array(get(get(first, field), "parts"))) != 1 || len(array(get(get(second, field), "parts"))) != 0 || len(array(get(get(event, field), "parts"))) != 1 {
				t.Fatal("delivery did not independently recheck and clone")
			}
			if kind == "message.received" && get(get(second, field), "status") != "recalled" {
				t.Fatal("status not recalled")
			}
		})
	}
}
