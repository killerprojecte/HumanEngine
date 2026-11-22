package botruntime

import (
	"context"
	"regexp"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func TestSentReceiptRetainsExplicitIDAndOnlyGeneratesForNil(t *testing.T) {
	for _, tt := range []struct {
		name string
		id   *string
	}{
		{"adapter ID", ptr("adapter-receipt")},
		{"explicit empty ID", ptr("")},
		{"missing ID", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
				switch req.Stage {
				case "decision":
					done(fixtureDecision("reply"), nil)
				case "plan":
					done(fixturePlan(req, "reply", nil), nil)
				default:
					done(completion([]replyplan.ToolCall{fixtureSend("Synthetic response.", false)}, ""), nil)
				}
			}))
			topic := registerFixture(t, r)
			r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
				if err := before(); err != nil {
					return dispatch.Rejected[dispatch.Receipt](err)
				}
				return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: tt.id})
			}}})
			h, err := r.Run("scene", topic, false)
			if err != nil {
				t.Fatal(err)
			}
			if result := awaitTurn(t, h); result.Counts["sentSegments"] != 1 {
				t.Fatalf("expected one acknowledged send: %+v", result)
			}
			snapshot, err := r.Snapshot("scene")
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, m := range snapshot.Messages {
				if get(m, "senderId") == "bot" {
					ids = append(ids, idOf(m))
				}
			}
			if len(ids) != 1 {
				t.Fatalf("expected one bot row: %q", ids)
			}
			if tt.id == nil {
				if matched, err := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, ids[0]); err != nil || !matched {
					t.Fatalf("missing ID did not get an RFC 9562 v4 UUID: %q (%v)", ids[0], err)
				}
			} else if got := ids[0]; got != *tt.id {
				t.Fatalf("adapter ID changed: got %q, want %q", got, *tt.id)
			}
		})
	}
}
