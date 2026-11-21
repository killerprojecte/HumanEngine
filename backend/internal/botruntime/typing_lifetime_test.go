package botruntime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func TestTypingGenerationOwnsCompletePreparedSend(t *testing.T) {
	for _, boundary := range []string{"pre-send-hook", "draft-review", "before-first-transmission", "after-transmission"} {
		t.Run(boundary, func(t *testing.T) {
			held := make(chan func(), 1)
			var r *Runtime
			r, _, _ = testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
				switch req.Stage {
				case "decision":
					done(fixtureDecision("reply"), nil)
				case "plan":
					done(fixturePlan(req, "reply", nil), nil)
				case "replyer":
					if boundary == "draft-review" {
						if _, err := r.Ingest(context.Background(), "scene", fixtureMessage("scene", "new", "New same-topic detail."), false); err != nil {
							t.Error(err)
						}
					}
					done(completion([]replyplan.ToolCall{fixtureSend("Exact prepared reply.", false)}, ""), nil)
				case "validation":
					held <- func() { done(completion(nil, ""), nil) }
				}
			}))
			topic := registerFixture(t, r)
			life, retire := context.WithCancel(context.Background())
			defer retire()
			delay := func(dispatch.Segment, *dispatch.Signal) *dispatch.Future[struct{}] {
				return dispatch.Resolved(struct{}{})
			}
			detach, err := r.InstallTypingDelay(life, delay)
			if err != nil {
				t.Fatal(err)
			}
			defer detach()
			var replacementCalls, wire atomic.Int32
			if boundary == "pre-send-hook" {
				r.AddHook(Hook{ID: "typing-lifetime-barrier", Run: func(_ context.Context, segments []dispatch.Segment, _ *TurnContext, done func(HookResult, error)) {
					held <- func() { done(HookResult{Segments: segments}, nil) }
				}})
			}
			adapter := &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
				future := dispatch.NewFuture[dispatch.Receipt]()
				transmit := func() {
					if err := before(); err != nil {
						future.Resolve(dispatch.Receipt{Status: dispatch.Cancelled})
						return
					}
					wire.Add(1)
					if boundary != "after-transmission" {
						future.Resolve(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr(newID())})
					}
				}
				switch boundary {
				case "before-first-transmission":
					held <- transmit
				case "after-transmission":
					transmit()
					held <- func() {}
				default:
					transmit()
				}
				return future
			}}
			if boundary == "before-first-transmission" {
				adapter = dispatch.AbortContainedAdapter(adapter)
			}
			r.AddAdapter("fixture", &Adapter{Transport: adapter})
			turn, err := r.Run("scene", topic, false)
			if err != nil {
				t.Fatal(err)
			}
			var resume func()
			select {
			case resume = <-held:
			case <-time.After(3 * time.Second):
				t.Fatal("send did not reach held phase")
			}
			retire()
			detach()
			replacement, stopReplacement := context.WithCancel(context.Background())
			defer stopReplacement()
			remove, err := r.InstallTypingDelay(replacement, func(segment dispatch.Segment, signal *dispatch.Signal) *dispatch.Future[struct{}] {
				replacementCalls.Add(1)
				return delay(segment, signal)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer remove()
			resume()
			result := awaitTurn(t, turn)
			if replacementCalls.Load() != 0 || result.Counts["sentSegments"] != 0 {
				t.Fatalf("retired draft used replacement or sent: %+v", result)
			}
			if boundary == "after-transmission" {
				if wire.Load() != 1 || result.Counts["unknownSegments"] != 1 {
					t.Fatalf("in-flight transport was not unknown: calls=%d result=%+v", wire.Load(), result)
				}
			} else if wire.Load() != 0 || result.Counts["cancelledSegments"] != 1 {
				t.Fatalf("retired draft transmitted or lost cancellation: calls=%d result=%+v", wire.Load(), result)
			}
		})
	}
}
