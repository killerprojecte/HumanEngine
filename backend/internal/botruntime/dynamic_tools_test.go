package botruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDynamicToolGetterIsolationAndTiming(t *testing.T) {
	var enabled atomic.Bool
	enabled.Store(true)
	var calls atomic.Int64
	e := &Extension{ToolsForTurn: func(c *TurnContext) []*Object {
		calls.Add(1)
		if !enabled.Load() {
			return nil
		}
		return []*Object{obj("name", c.TurnID)}
	}}
	a, b := &TurnContext{TurnID: "turn-a"}, &TurnContext{TurnID: "turn-b"}
	advertised := extensionTools(e, a)
	enabled.Store(false)
	if len(extensionTools(e, a)) != 0 {
		t.Fatal("getter reused stale configuration")
	}
	if len(advertised) != 1 || get(advertised[0], "name") != "turn-a" {
		t.Fatal("new lookup mutated advertised definitions")
	}
	enabled.Store(true)
	var wg sync.WaitGroup
	failures := make(chan string, 2)
	for _, c := range []*TurnContext{a, b} {
		wg.Add(1)
		go func(c *TurnContext) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				tools := extensionTools(e, c)
				if len(tools) != 1 || get(tools[0], "name") != c.TurnID {
					failures <- fmt.Sprint(c.TurnID)
					return
				}
			}
		}(c)
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Fatal("tool set crossed turns", failure)
	}
	if calls.Load() != 202 {
		t.Fatalf("unexpected helper invocation count: %d", calls.Load())
	}
}
func TestDynamicSupplierRegistrationRetainsStaticClone(t *testing.T) {
	r := new(Runtime)
	tool := obj("name", "original")
	remove := r.AddExtension(&Extension{Tools: []*Object{tool}})
	_ = tool.Set("name", "changed")
	registered := r.exts()
	if len(registered) != 1 || get(extensionTools(registered[0], nil)[0], "name") != "original" {
		t.Fatal("static tool lost registration clone")
	}
	remove()
	if len(r.exts()) != 0 {
		t.Fatal("removed extension still advertised")
	}
}

func TestDynamicToolsActualSourceRuntime(t *testing.T) {
	raw, err := os.ReadFile("testdata/dynamic-tools-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Outcomes []struct {
			Scenario                                             string
			Trace                                                []string
			Executes, Sends, Deliveries, CustomSends, ImageCalls int
			ToolEvents                                           []any
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixture.Outcomes {
		t.Run(row.Scenario, func(t *testing.T) {
			var enabled atomic.Bool
			enabled.Store(row.Scenario != "enable-unadvertised" && row.Scenario != "enable-after-decision")
			var mu sync.Mutex
			trace := []string{}
			toolEvents := []any{}
			record := func(s string) { mu.Lock(); trace = append(trace, s); mu.Unlock() }
			var executes, sends, deliveries, customSends, imageCalls atomic.Int64
			var sendEffect atomic.Value
			sendEffect.Store("send")
			var scene Scene
			var removeImages func()
			plans := 0
			var runtime *Runtime
			model := ModelFunc(func(ctx context.Context, req ModelRequest, done func(Completion, error)) {
				record(fmt.Sprintf("model:%s:%t", req.Role, toolNamed(req.Tools, "fixture.read") != nil))
				switch req.Stage {
				case "decision":
					if row.Scenario == "enable-after-decision" {
						enabled.Store(true)
					}
					if strings.Contains(row.Scenario, "learning-after-decision") {
						next := scene
						next.LearningEnabled = row.Scenario == "enable-learning-after-decision"
						if e := runtime.RegisterScene(next); e != nil {
							t.Error(e)
						}
					}
					done(fixtureDecision("reply"), nil)
				case "plan":
					if strings.Contains(row.Scenario, "learning-after-decision") {
						found := false
						for _, tool := range array(get(requestData(req), "actionTools")) {
							if get(tool, "name") == "learning.propose" {
								found = true
							}
						}
						record(fmt.Sprint("learning-actions:", found))
					}
					plans++
					if plans == 1 {
						if row.Scenario == "revoke-before-execute" {
							enabled.Store(false)
						}
						if row.Scenario == "enable-unadvertised" {
							enabled.Store(true)
						}
						if row.Scenario == "cancel-before-execute" {
							runtime.Cancel("scene")
						}
						if row.Scenario == "recall-before-execute" {
							if e := runtime.Recall(ctx, "scene", "source"); e != nil {
								t.Error(e)
							}
						}
						if row.Scenario == "remove-history-images" {
							done(completion([]replyplan.ToolCall{toolCall("messages.around", obj("messageId", array(get(get(requestData(req), "planContract"), "targetMessageIds"))[0], "limit", float64(1)))}, ""), nil)
						} else {
							done(completion([]replyplan.ToolCall{toolCall("fixture.read", obj())}, ""), nil)
						}
					} else {
						planned := fixturePlan(req, "reply", []any{})
						if strings.HasPrefix(row.Scenario, "send-effect-") {
							_ = planned.ToolCalls[0].Arguments.(*Object).Set("deliveryTool", "fixture.send")
						}
						done(planned, nil)
					}
				case "replyer":
					if strings.HasPrefix(row.Scenario, "send-effect-") {
						for _, m := range req.Messages {
							if get(m, "role") == "tool" {
								done(completion(nil, ""), nil)
								return
							}
						}
						if row.Scenario == "send-effect-read" {
							sendEffect.Store("read")
						} else {
							sendEffect.Store("learningCandidate")
						}
						done(completion([]replyplan.ToolCall{toolCall("fixture.send", obj())}, ""), nil)
					} else {
						done(completion([]replyplan.ToolCall{fixtureSend("Synthetic", false)}, ""), nil)
					}
				default:
					done(Completion{}, fmt.Errorf("unexpected stage %s", req.Stage))
				}
			})
			r, _, _ := testRuntime(t, model)
			runtime = r
			r.services.Trace = func(e Event) {
				if e.Stage != "tool" || e.ToolName != "fixture.read" {
					return
				}
				value := map[string]any{"status": e.Status, "reason": e.Reason}
				if e.Counts != nil {
					value["counts"] = e.Counts
				}
				if e.ReceiptState != "" {
					value["receiptState"] = e.ReceiptState
				}
				mu.Lock()
				toolEvents = append(toolEvents, decoded(value))
				mu.Unlock()
			}
			cfg := r.config()
			cfg.PromptContextWindowEnabled = row.Scenario == "stable-window" || row.Scenario == "remove-window"
			if err = r.Configure(cfg); err != nil {
				t.Fatal(err)
			}
			scene = fixtureScene(false)
			scene.LearningEnabled = row.Scenario == "disable-learning-after-decision"
			if err = r.RegisterScene(scene); err != nil {
				t.Fatal(err)
			}
			r.AddAdapter("fixture", &Adapter{History: func(_ context.Context, _ Scene, _ int, done func(*Object, error)) {
				if row.Scenario == "remove-history-images" {
					record("history-entered")
					removeImages()
					record("history-returned")
				}
				done(obj("items", []any{}, "coverage", "partial"), nil)
			}, GetMessage: func(ctx context.Context, _ Scene, _ string, done func(*Object, error)) {
				if row.Scenario == "remove-history-images" {
					record("history-entered")
					removeImages()
					record("history-returned")
				}
				done(obj("items", []any{}, "coverage", "partial"), nil)
			}, Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
				if e := before(); e != nil {
					return dispatch.Rejected[dispatch.Receipt](e)
				}
				sends.Add(1)
				return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr("sent")})
			}}})
			var remove func()
			primary := &Extension{ToolsForTurn: func(*TurnContext) []*Object {
				on := enabled.Load()
				record(fmt.Sprint("getter:", on))
				if !on {
					return nil
				}
				tools := []*Object{obj("name", "fixture.read", "description", "Fixture read", "effect", "read", "inputSchema", obj("type", "object", "properties", obj()))}
				if strings.HasPrefix(row.Scenario, "send-effect-") {
					tools = append(tools, obj("name", "fixture.send", "description", "Fixture send", "effect", sendEffect.Load().(string), "inputSchema", obj("type", "object", "properties", obj())))
				}
				if strings.Contains(row.Scenario, "learning-after-decision") {
					tools = append(tools, obj("name", "learning.propose", "description", "Fixture learning", "effect", "learningCandidate", "inputSchema", obj("type", "object", "properties", obj())))
				}
				return tools
			}, Delivered: func(_ string, value any, _ *TurnContext, _ any) (any, error) { deliveries.Add(1); return value, nil }, Execute: func(_ context.Context, name string, _ any, _ *TurnContext, done func(any, error)) {
				if name == "fixture.send" {
					customSends.Add(1)
					done([]any{}, nil)
					return
				}
				executes.Add(1)
				record("execute")
				if row.Scenario == "remove-before-deliver" || row.Scenario == "remove-window" {
					remove()
				}
				if row.Scenario == "tool-rejected" {
					done(obj("status", "rejected"), nil)
				} else if row.Scenario == "tool-queued" {
					done(obj("status", "queued"), nil)
				} else {
					done(obj("value", "synthetic"), nil)
				}
			}}
			if row.Scenario == "duplicate-name-no-hook" {
				primary.Delivered = nil
			}
			remove = r.AddExtension(primary)
			if row.Scenario == "duplicate-name-no-hook" {
				r.AddExtension(&Extension{Tools: []*Object{obj("name", "fixture.read", "description", "Duplicate", "effect", "read", "inputSchema", obj("type", "object", "properties", obj()))}, Delivered: func(_ string, value any, _ *TurnContext, _ any) (any, error) {
					deliveries.Add(1)
					record("duplicate-delivered")
					return value, nil
				}})
			}
			removeImages = r.AddExtension(&Extension{DescribeImages: func(_ context.Context, _ *TurnContext, _ []*Object, done func(any, error)) {
				if row.Scenario == "remove-history-images" {
					imageCalls.Add(1)
					record("describe-images")
				}
				done(nil, nil)
			}})
			topic, e := r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "source", "Please reply."), false)
			if e != nil {
				t.Fatal(e)
			}
			handle, e := r.Run(scene.ID, topic, false)
			if e != nil {
				t.Fatal(e)
			}
			select {
			case <-handle.Done:
			case <-time.After(5 * time.Second):
				t.Fatal("dynamic source turn blocked")
			}
			_ = handle.Wait()
			want := []string{}
			for _, event := range row.Trace {
				if strings.HasPrefix(event, "getter:") {
					pieces := strings.Split(event, ":")
					event = "getter:" + pieces[len(pieces)-1]
				}
				want = append(want, event)
			}
			mu.Lock()
			got := append([]string{}, trace...)
			mu.Unlock()
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("source getter timing differs\ngot %v\nwant %v", got, want)
			}
			mu.Lock()
			actualEvents := append([]any{}, toolEvents...)
			mu.Unlock()
			if !reflect.DeepEqual(actualEvents, row.ToolEvents) {
				t.Fatalf("source completion events differ got=%v want=%v", actualEvents, row.ToolEvents)
			}
			if executes.Load() != int64(row.Executes) || sends.Load() != int64(row.Sends) || deliveries.Load() != int64(row.Deliveries) || customSends.Load() != int64(row.CustomSends) || imageCalls.Load() != int64(row.ImageCalls) {
				t.Fatalf("source outcome differs execute=%d send=%d", executes.Load(), sends.Load())
			}
		})
	}
}

func TestDynamicToolOutcomeActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/dynamic-tools-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		TypedOutcomes []struct {
			Input  json.RawMessage
			Result any
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixture.TypedOutcomes {
		input, e := jsjson.Parse(string(row.Input))
		if e != nil {
			t.Fatal(e)
		}
		outcome := toolCompletionOutcome(input)
		details := map[string]any{"counts": outcome.Counts, "reason": outcome.Reason}
		if outcome.ReceiptState != "" {
			details["receiptState"] = outcome.ReceiptState
		}
		got := decoded(map[string]any{"status": outcome.Status, "details": details})
		if !reflect.DeepEqual(got, row.Result) {
			t.Fatalf("source toolOutcome differs input=%s got=%v want=%v", row.Input, got, row.Result)
		}
	}
}
