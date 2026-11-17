package botruntime

import (
	"context"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reviewCall struct {
	req  ModelRequest
	done func(Completion, error)
}

func waitReview(t *testing.T, calls <-chan reviewCall) reviewCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("review did not start")
		return reviewCall{}
	}
}

func TestRetainedDraftOneReviewAndIndependentBudget(t *testing.T) {
	for _, mode := range []string{"direct", "other-topic", "complete", "cancel", "wrong-draft", "failure", "timeout", "repeated", "unrelated-recall", "review-input-recall", "quoted-recall", "shutdown", "authority-error", "authority-read-error"} {
		t.Run(mode, func(t *testing.T) {
			var r *Runtime
			var replies, reviewCount atomic.Int64
			calls := make(chan reviewCall, 1)
			r, authority, clock := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
				switch req.Stage {
				case "decision":
					done(fixtureDecision("reply"), nil)
				case "plan":
					done(fixturePlan(req, "reply", nil), nil)
				case "replyer":
					replies.Add(1)
					if mode != "direct" {
						text := "New input"
						if mode == "other-topic" {
							text = "[topic:other] Unrelated topic"
						}
						if _, e := r.Ingest(context.Background(), "scene", fixtureMessage("scene", "new-input", text), false); e != nil {
							t.Error(e)
						}
					}
					done(completion([]replyplan.ToolCall{fixtureSend("Exact retained answer.", true)}, ""), nil)
				case "validation":
					reviewCount.Add(1)
					calls <- reviewCall{req, done}
				}
			}))
			cfg := r.config()
			cfg.ReplyerTimeoutMS = 10
			cfg.PlannerTimeoutMS = 100
			if mode == "other-topic" {
				cfg.TopicAdmissionEnabled = true
			}
			if e := r.Configure(cfg); e != nil {
				t.Fatal(e)
			}
			if err := r.RegisterScene(fixtureScene(false)); err != nil {
				t.Fatal(err)
			}
			topic, err := r.Ingest(context.Background(), "scene", fixtureMessage("scene", "source", "[topic:main] Initial question"), false)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = r.Ingest(context.Background(), "scene", fixtureMessage("scene", "background", "Ordinary old context"), false)
			var sent []dispatch.Segment
			var mu sync.Mutex
			r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, segment dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
				if e := before(); e != nil {
					return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Cancelled})
				}
				mu.Lock()
				sent = append(sent, cloneSegments([]dispatch.Segment{segment})[0])
				mu.Unlock()
				return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr(newID())})
			}}})
			var failures []string
			r.services.Trace = func(e Event) {
				if e.Stage == "validation" && e.Status == "failed" {
					mu.Lock()
					failures = append(failures, e.Reason)
					mu.Unlock()
				}
			}
			h, e := r.Run("scene", topic, false)
			if e != nil {
				t.Fatal(e)
			}
			if mode != "direct" && mode != "other-topic" {
				call := waitReview(t, calls)
				data := requestData(call.req)
				if len(call.req.Tools) != 1 || get(call.req.Tools[0], "name") != "reply.cancel" {
					t.Fatal("review offered rewriting", call.req.Tools)
				}
				if got := len(array(get(get(data, "proposed"), "segments"))); got != 2 {
					t.Fatalf("review lost full draft: %d", got)
				}
				if len(array(get(data, "newMessages"))) != 1 {
					t.Fatal("review included old context as new input")
				}
				// Original Replyer deadline expires while its completed draft remains owned.
				clock.Advance(11)
				switch mode {
				case "cancel":
					call.done(completion([]replyplan.ToolCall{toolCall("reply.cancel", obj("draftId", get(data, "draftId"), "reason", "new correction"))}, ""), nil)
				case "wrong-draft":
					call.done(completion([]replyplan.ToolCall{toolCall("reply.cancel", obj("draftId", "previous-generation", "reason", "wrong"))}, ""), nil)
				case "failure":
					call.done(Completion{}, code("provider_unavailable"))
				case "timeout":
					clock.Advance(90)
				case "repeated":
					for i := 0; i < 20; i++ {
						_, _ = r.Ingest(context.Background(), "scene", fixtureMessage("scene", fmt.Sprintf("later-%d", i), "Later input"), false)
					}
					call.done(completion(nil, ""), nil)
				case "unrelated-recall":
					_ = r.Recall(context.Background(), "scene", "background")
					call.done(completion(nil, ""), nil)
				case "review-input-recall":
					_ = r.Recall(context.Background(), "scene", "new-input")
					call.done(completion(nil, ""), nil)
				case "quoted-recall":
					_ = r.Recall(context.Background(), "scene", "source")
					call.done(completion(nil, ""), nil)
				case "shutdown":
					r.Cancel("scene")
					call.done(completion(nil, ""), nil)
				case "authority-read-error":
					call.req.Turn.AddScopedValidation(func(ReadAuthority) (bool, error) { return false, code("authority_storage_failed") })
					call.done(Completion{}, code("provider_unavailable"))
				case "authority-error":
					authority.mu.Lock()
					authority.readyErr = code("permission_revoked")
					authority.mu.Unlock()
					call.done(Completion{}, code("provider_unavailable"))
				default:
					call.done(completion(nil, ""), nil)
				}
			}
			result := awaitTurn(t, h)
			want := 2
			if contains([]string{"cancel", "quoted-recall", "shutdown", "authority-error", "authority-read-error"}, mode) {
				want = 0
			}
			mu.Lock()
			got := len(sent)
			payload := append([]dispatch.Segment{}, sent...)
			logged := append([]string{}, failures...)
			mu.Unlock()
			if got != want {
				t.Fatalf("sent=%d want=%d result=%+v", got, want, result)
			}
			if want == 2 {
				expected := segmentsFrom(get(fixtureSend("Exact retained answer.", true).Arguments, "segments"))
				if !reflect.DeepEqual(payload, expected) {
					t.Fatalf("draft rewritten: %+v", payload)
				}
			}
			wantReviews := int64(1)
			if mode == "direct" || mode == "other-topic" {
				wantReviews = 0
			}
			if reviewCount.Load() != wantReviews || replies.Load() != 1 {
				t.Fatalf("review/reply calls=%d/%d", reviewCount.Load(), replies.Load())
			}
			if mode == "failure" && !contains(logged, "draft_review_failed") {
				t.Fatal(logged)
			}
			if mode == "timeout" && !contains(logged, "draft_review_timeout") {
				t.Fatal(logged)
			}
		})
	}
}

func TestRetainedDraftRecallOnlyReferencesAtCompletedBoundary(t *testing.T) {
	for _, target := range []string{"background", "source", "cited-background"} {
		t.Run(target, func(t *testing.T) {
			entered := make(chan func(HookResult, error), 1)
			var reviews, sends atomic.Int64
			r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
				switch req.Stage {
				case "decision":
					done(fixtureDecision("reply"), nil)
				case "plan":
					planned := fixturePlan(req, "reply", nil)
					if target == "cited-background" {
						for _, evidence := range array(get(requestData(req), "evidence")) {
							if get(get(evidence, "value"), "id") == "background" {
								_ = planned.ToolCalls[0].Arguments.(*Object).Set("facts", []any{obj("text", "Cited background fact.", "evidenceIds", []any{get(evidence, "id")})})
							}
						}
					}
					done(planned, nil)
				case "validation":
					reviews.Add(1)
					done(completion(nil, ""), nil)
				default:
					done(completion([]replyplan.ToolCall{fixtureSend("Unchanged.", false)}, ""), nil)
				}
			}))
			topic := registerFixture(t, r)
			_, _ = r.Ingest(context.Background(), "scene", fixtureMessage("scene", "background", "Old context"), false)
			var draft []dispatch.Segment
			r.AddHook(Hook{ID: "completed-barrier", Run: func(_ context.Context, segments []dispatch.Segment, _ *TurnContext, done func(HookResult, error)) {
				draft = segments
				entered <- done
			}})
			r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, _ func() error) *dispatch.Future[dispatch.Receipt] {
				sends.Add(1)
				return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent})
			}}})
			h, _ := r.Run("scene", topic, false)
			var done func(HookResult, error)
			select {
			case done = <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("draft hook missing")
			}
			recalled := target
			if target == "cited-background" {
				recalled = "background"
			}
			if e := r.Recall(context.Background(), "scene", recalled); e != nil {
				t.Fatal(e)
			}
			done(HookResult{Segments: draft}, nil)
			result := awaitTurn(t, h)
			want := int64(1)
			if target == "source" || target == "cited-background" {
				want = 0
			}
			if sends.Load() != want || reviews.Load() != 0 {
				t.Fatalf("sends/reviews %d/%d result=%+v", sends.Load(), reviews.Load(), result)
			}
		})
	}
}

func TestRetainedDraftTransmitTimeoutIsUnknownAndDoesNotReplay(t *testing.T) {
	entered := make(chan struct{}, 1)
	var sends atomic.Int64
	r, _, clock := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		switch req.Stage {
		case "decision":
			done(fixtureDecision("reply"), nil)
		case "plan":
			done(fixturePlan(req, "reply", nil), nil)
		default:
			done(completion([]replyplan.ToolCall{fixtureSend("Exact.", true)}, ""), nil)
		}
	}))
	cfg := r.config()
	cfg.ReplyerTimeoutMS = 10
	_ = r.Configure(cfg)
	topic := registerFixture(t, r)
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, _ func() error) *dispatch.Future[dispatch.Receipt] {
		sends.Add(1)
		entered <- struct{}{}
		return dispatch.NewFuture[dispatch.Receipt]()
	}}})
	h, _ := r.Run("scene", topic, false)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("not transmitted")
	}
	clock.Advance(11)
	result := awaitTurn(t, h)
	if sends.Load() != 1 || result.Counts["unknownSegments"] != 1 {
		t.Fatalf("unknown send replayed: %+v calls=%d", result, sends.Load())
	}
}

func TestRetainedDraftIngressDuringPhysicalSendDoesNotTruncate(t *testing.T) {
	var r *Runtime
	var sends, reviews atomic.Int64
	r, _, _ = testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		switch req.Stage {
		case "decision":
			done(fixtureDecision("reply"), nil)
		case "plan":
			done(fixturePlan(req, "reply", nil), nil)
		case "validation":
			reviews.Add(1)
			done(completion(nil, ""), nil)
		default:
			done(completion([]replyplan.ToolCall{fixtureSend("Exact before later ingress.", true)}, ""), nil)
		}
	}))
	topic := registerFixture(t, r)
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		if sends.Add(1) == 1 {
			_, _ = r.Ingest(context.Background(), "scene", fixtureMessage("scene", "during-send", "Later input"), false)
		}
		if err := before(); err != nil {
			t.Error(err)
			return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Cancelled})
		}
		return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr(newID())})
	}}})
	h, _ := r.Run("scene", topic, false)
	result := awaitTurn(t, h)
	if sends.Load() != 2 || reviews.Load() != 0 || result.Counts["sentSegments"] != 2 {
		t.Fatalf("sealed draft truncated: %+v sends=%d reviews=%d", result, sends.Load(), reviews.Load())
	}
}

func TestRetainedDraftGroundingRepairKeepsOriginalGenerationDeadline(t *testing.T) {
	var replies atomic.Int64
	repair := make(chan struct{}, 1)
	r, _, clock := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		switch req.Stage {
		case "decision":
			done(fixtureDecision("reply"), nil)
		case "plan":
			done(fixturePlan(req, "reply", nil), nil)
		default:
			if replies.Add(1) == 1 {
				done(completion([]replyplan.ToolCall{fixtureSend("I will send you another image tomorrow.", false)}, ""), nil)
			} else {
				repair <- struct{}{}
			}
		}
	}))
	cfg := r.config()
	cfg.ReplyerTimeoutMS = 10
	_ = r.Configure(cfg)
	topic := registerFixture(t, r)
	h, _ := r.Run("scene", topic, false)
	select {
	case <-repair:
	case <-time.After(3 * time.Second):
		t.Fatal("repair not entered")
	}
	clock.Advance(11)
	result := awaitTurn(t, h)
	if replies.Load() != 2 || result.Status != "failed" || result.Counts["sentSegments"] != 0 {
		t.Fatal(result, replies.Load())
	}
}
