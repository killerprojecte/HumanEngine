package botruntime

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The bot branch intentionally does not enroll or erase its caller source ref,
// just as Runtime.ingest. Observe is the user-source domain, not identity indexing.
func TestBotIngressMatchesActualSourceAuthorityBoundary(t *testing.T) {
	data, err := os.ReadFile("testdata/ingress.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, SenderID string
		Enabled        bool
		Expected       map[string]any
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			r, a, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Error("unexpected model") }))
			a.enrolled = c.Enabled
			scene := fixtureScene(false)
			scene.ID = "ingress-scene"
			if err := r.RegisterScene(scene); err != nil {
				t.Fatal(err)
			}
			r.AddAdapter("fixture", &Adapter{BotUserID: "platform-bot"})
			extensions := 0
			events := []string{}
			r.AddExtension(&Extension{Received: func(*Object, Scene) error { extensions++; return nil }})
			r.services.Trace = func(e Event) {
				if e.Stage == "ingress" && e.Reason == "message_received" {
					events = append(events, "message.received")
				}
			}
			m := fixtureMessage(scene.ID, "source", "Synthetic ingress")
			_ = m.Set("senderId", c.SenderID)
			_ = m.Set("sourceEventRef", obj("eventId", "caller-ref", "revision", float64(99), "deletionEpoch", float64(0)))
			if _, err := r.Ingest(context.Background(), scene.ID, m, false); err != nil {
				t.Fatal(err)
			}
			view, err := r.Snapshot(scene.ID)
			if err != nil {
				t.Fatal(err)
			}
			// Test authority observes ordinary users even when enrollment is disabled;
			// the durable bridge then bypasses sourceEvent observe as the source does.
			calls := a.writes
			if !c.Enabled {
				calls = 0
			}
			if c.SenderID != "user" && a.writes != 0 {
				t.Fatal("bot enrolled in source authority")
			}
			actual := map[string]any{"observeCalls": calls, "extensionCalls": extensions, "senderId": get(view.Messages[0], "senderId"), "keptCallerRef": get(get(view.Messages[0], "sourceEventRef"), "eventId") == "caller-ref", "events": events, "topicCount": len(view.Topics)}
			if !reflect.DeepEqual(decoded(actual), c.Expected) {
				t.Fatalf("actual=%v source=%v", actual, c.Expected)
			}
		})
	}
}
