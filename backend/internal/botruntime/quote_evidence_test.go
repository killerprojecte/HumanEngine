package botruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func TestExplicitBotQuoteSupportsPlannerAndReplyer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		visible bool
		reads   int
	}{
		{"cached", true, 0}, {"history", true, 1}, {"missing", false, 1}, {"recalled", false, 0}, {"foreign", false, 1}, {"recall-during-fetch", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var planner, replyer *Object
			var sent *string
			r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
				switch req.Stage {
				case "decision":
					done(fixtureDecision("reply"), nil)
				case "plan":
					planner = requestData(req)
					plan := fixturePlan(req, "reply", nil)
					args := plan.ToolCalls[0].Arguments.(*Object)
					_ = args.Set("contextMessageIds", get(args, "targetMessageIds"))
					done(completion(plan.ToolCalls, ""), nil)
				case "replyer":
					replyer = requestData(req)
					done(completion([]replyplan.ToolCall{fixtureSend("Acknowledged the current message.", false)}, ""), nil)
				default:
					done(Completion{}, code("unexpected_stage"))
				}
			}))
			if err := r.RegisterScene(fixtureScene(false)); err != nil {
				t.Fatal(err)
			}
			quote := fixtureMessage("scene", "quoted-bot", "original bot statement")
			_ = quote.Set("senderId", "bot")
			if tc.name == "cached" {
				if _, err := r.Ingest(t.Context(), "scene", quote, false); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "recalled" {
				if err := r.Recall(t.Context(), "scene", idOf(quote)); err != nil {
					t.Fatal(err)
				}
			}
			incoming := fixtureMessage("scene", "source", "Please explain that.")
			_ = incoming.Set("replyToId", idOf(quote))
			topic, err := r.Ingest(t.Context(), "scene", incoming, false)
			if err != nil {
				t.Fatal(err)
			}
			readCalls := 0
			held := make(chan func(*Object, error), 1)
			r.AddAdapter("fixture", &Adapter{
				GetMessage: func(_ context.Context, _ Scene, id string, complete func(*Object, error)) {
					readCalls++
					if id != idOf(quote) {
						t.Errorf("unexpected quote ID %q", id)
					}
					if tc.name == "missing" {
						complete(obj("items", []any{}), nil)
						return
					}
					if tc.name == "recall-during-fetch" {
						held <- complete
						return
					}
					row := quote.Clone()
					if tc.name == "foreign" {
						_ = row.Set("sceneId", "foreign")
					}
					_ = row.Set("origin", "history")
					_ = row.Set("receivedAt", "2026-10-20T00:00:00.000Z")
					complete(obj("items", []any{row}), nil)
				},
				Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, segment dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
					if err := before(); err != nil {
						return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Cancelled})
					}
					sent = segment.ReplyToID
					return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr("sent")})
				}},
			})
			handle, err := r.Run("scene", topic, false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "recall-during-fetch" {
				select {
				case complete := <-held:
					if err := r.Recall(t.Context(), "scene", idOf(quote)); err != nil {
						t.Fatal(err)
					}
					complete(obj("items", []any{quote}), nil)
				case <-time.After(3 * time.Second):
					t.Fatal("quote fetch was not held")
				}
			}
			result := awaitTurn(t, handle)
			if readCalls != tc.reads || sent == nil || *sent != "source" || planner == nil || replyer == nil {
				t.Fatalf("reads=%d want=%d sent=%v result=%+v", readCalls, tc.reads, sent, result)
			}
			for _, prompt := range []*Object{planner, replyer} {
				body, err := jsjson.Stringify(prompt)
				if err != nil || strings.Contains(body, "original bot statement") != tc.visible {
					t.Fatalf("quote visibility differs in %s: %v", body, err)
				}
			}
		})
	}
}
