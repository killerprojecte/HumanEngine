package botruntime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

func TestClosePreservesAcknowledgedAndUnknownInflightReceipts(t *testing.T) {
	inflight := make(chan *dispatch.Future[dispatch.Receipt], 1)
	var calls, replies atomic.Int32
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		switch req.Stage {
		case "decision":
			done(fixtureDecision("reply"), nil)
		case "plan":
			done(fixturePlan(req, "reply", nil), nil)
		default:
			done(completion([]replyplan.ToolCall{fixtureSend("Synthetic first.", replies.Add(1) == 2)}, ""), nil)
		}
	}))
	topic := registerFixture(t, r)
	r.AddAdapter("fixture", &Adapter{Transport: dispatch.AbortContainedAdapter(&dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		if err := before(); err != nil {
			return dispatch.Rejected[dispatch.Receipt](err)
		}
		switch calls.Add(1) {
		case 1:
			return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr("earlier-turn")})
		case 2:
			return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr("acked")})
		}
		f := dispatch.NewFuture[dispatch.Receipt]()
		inflight <- f
		return f
	}})})
	h, err := r.Run("scene", topic, false)
	if err != nil {
		t.Fatal(err)
	}
	if result := awaitTurn(t, h); result.Counts["sentSegments"] != 1 {
		t.Fatalf("earlier turn did not finish: %+v", result)
	}
	h, err = r.Run("scene", topic, false)
	if err != nil {
		t.Fatal(err)
	}
	var second *dispatch.Future[dispatch.Receipt]
	select {
	case second = <-inflight:
	case <-time.After(2 * time.Second):
		t.Fatal("did not reach second physical submission")
	}
	r.operationMu.Lock()
	retired, active := r.operationFirst.owner, r.operationLast.owner
	initialLength := r.operations.Len()
	r.operationMu.Unlock()
	if retired == active || initialLength != 2 {
		t.Fatalf("expected a finished and an active turn: operations=%d", initialLength)
	}
	// The contained adapter, not context cancellation, owns this physical
	// outcome. Release it only after non-waiting Close has returned.
	r.Close()
	r.operationMu.Lock()
	remaining, retiredLength := r.operations.Len(), retired.operations.Len()
	r.operationMu.Unlock()
	second.Resolve(dispatch.Receipt{Status: dispatch.Unknown, Detail: ptr("delivery_unknown")})
	result := awaitTurn(t, h)
	if result.Status != "partial" || result.Reason != "delivery_unknown" ||
		result.Counts["sentSegments"] != 1 || result.Counts["unknownSegments"] != 1 ||
		len(result.Receipts) != 2 || result.Receipts[0].Status != dispatch.Sent || result.Receipts[1].Status != dispatch.Unknown || calls.Load() != 3 {
		t.Fatalf("lost physical outcomes during shutdown: status=%s reason=%s counts=%v receipts=%v calls=%d",
			result.Status, result.Reason, result.Counts, result.Receipts, calls.Load())
	}
	if remaining != 1 || retiredLength != 0 {
		t.Fatalf("non-waiting Close kept the wrong operations: remaining=%d retired=%d", remaining, retiredLength)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := r.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	if r.operations.Len() != 0 || active.operations.Len() != 0 || active.outcomeCount != 0 ||
		len(active.state.operationKeys) != 0 || r.operationFirst != nil || r.operationLast != nil {
		t.Fatal("receipt ownership survived the physical drain")
	}
}
