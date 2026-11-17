package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOutgoingScopeChecksFinalHookPayloadAndPhysicalTransmitGate(t *testing.T) {
	for _, mode := range []string{"post-hook-revoked", "transmit-revoked"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
				switch req.Stage {
				case "decision":
					done(fixtureDecision("reply"), nil)
				case "plan":
					done(fixturePlan(req, "reply", nil), nil)
				default:
					done(completion([]replyplan.ToolCall{fixtureSend("Before hook.", false)}, ""), nil)
				}
			}))
			var allowed atomic.Bool
			allowed.Store(mode != "post-hook-revoked")
			var mu sync.Mutex
			var checked []string
			r.AddExtension(&Extension{ValidateOutgoingScoped: func(_ ReadAuthority, _ *TurnContext, segments []dispatch.Segment) (bool, error) {
				mu.Lock()
				for _, segment := range segments {
					checked = append(checked, str(get(fromRawParts(segment.Parts)[0], "text")))
				}
				mu.Unlock()
				return allowed.Load(), nil
			}})
			r.AddHook(Hook{ID: "final-output", Run: func(_ context.Context, _ []dispatch.Segment, _ *TurnContext, done func(HookResult, error)) {
				done(HookResult{Segments: segmentsFrom(get(fixtureSend("After hook.", false).Arguments, "segments"))}, nil)
			}})
			topic := registerFixture(t, r)
			type pending struct {
				gate   func() error
				future *dispatch.Future[dispatch.Receipt]
			}
			entered := make(chan pending, 1)
			var sends atomic.Int64
			r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
				sends.Add(1)
				future := dispatch.NewFuture[dispatch.Receipt]()
				entered <- pending{before, future}
				return future
			}}})
			h, _ := r.Run("scene", topic, false)
			if mode == "transmit-revoked" {
				var send pending
				select {
				case send = <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("transmit preparation missing")
				}
				allowed.Store(false)
				if err := send.gate(); err == nil {
					t.Fatal("final outgoing authority was not rechecked")
				}
				send.future.Resolve(dispatch.Receipt{Status: dispatch.Cancelled, Detail: ptr("send_invalidated")})
			}
			result := awaitTurn(t, h)
			if result.Counts["sentSegments"] != 0 {
				t.Fatal(result)
			}
			if mode == "post-hook-revoked" && sends.Load() != 0 {
				t.Fatal("invalid final payload reached adapter")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(checked) == 0 {
				t.Fatal("outgoing scope not checked")
			}
			for _, text := range checked {
				if text != "After hook." {
					t.Fatalf("wrong payload checked: %q", text)
				}
			}
		})
	}
}
