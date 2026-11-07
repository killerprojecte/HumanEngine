package sourceevents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

type runtimeReadyFault struct {
	*RuntimeAuthority
	failed atomic.Bool
	once   atomic.Bool
	cause  error
}

func (a *runtimeReadyFault) Ready() error {
	if a.failed.Load() {
		return errors.New("recovery_synthetic_blocked")
	}
	return a.RuntimeAuthority.Ready()
}
func (a *runtimeReadyFault) ValidateSources(scene botruntime.Scene, rows []*jsjson.Object, ids []string) (bool, error) {
	if a.once.Swap(false) {
		return false, a.cause
	}
	return a.RuntimeAuthority.ValidateSources(scene, rows, ids)
}
func (a *runtimeReadyFault) IsRecalled(scene botruntime.Scene, id string) (bool, error) {
	if a.once.Swap(false) {
		return false, a.cause
	}
	return a.RuntimeAuthority.IsRecalled(scene, id)
}
func runtimeComplete(calls ...replyplan.ToolCall) botruntime.Completion {
	values := []any{}
	for _, c := range calls {
		values = append(values, obj("id", c.ID, "name", c.Name, "arguments", c.Arguments))
	}
	return botruntime.Completion{Completion: replyplan.Completion{ToolCalls: calls, Assistant: obj("role", "assistant", "content", "", "toolCalls", values)}}
}
func runtimeCall(name string, value any) replyplan.ToolCall {
	return replyplan.ToolCall{ID: name, Name: name, Arguments: value}
}
func runtimePlan(t *testing.T, request botruntime.ModelRequest) botruntime.Completion {
	t.Helper()
	data := new(jsjson.Object)
	for _, message := range request.Messages {
		if str(message, "role") == "user" {
			parsed, e := jsjson.Parse(str(message, "content"))
			if e != nil {
				t.Fatal(e)
			}
			if o, ok := parsed.(*jsjson.Object); ok {
				for _, member := range o.Members() {
					_ = data.Set(member.Key, member.Value)
				}
			}
		}
	}
	contract := get(data, "planContract").(*jsjson.Object)
	return runtimeComplete(runtimeCall("reply.plan", obj("action", "reply", "deliveryTool", "messages.send", "targetMessageIds", get(contract, "targetMessageIds"), "contextMessageIds", get(contract, "contextMessageIds"), "strategy", "Answer the selected fixture context.", "constraints", []any{}, "facts", []any{}, "assets", []any{}, "emoji", []any{}, "memorySelections", []any{}, "expressionGoals", []any{"Compose a concise fixture reply."}, "actions", []any{})))
}
func jsonDomain(t *testing.T, value any) any {
	t.Helper()
	raw, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	var result any
	if e = json.Unmarshal(raw, &result); e != nil {
		t.Fatal(e)
	}
	return result
}
func TestRuntimeSourceAuthorityActualFaultWindows(t *testing.T) {
	raw, e := os.ReadFile("testdata/runtime-authority-faults.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures struct {
		Cases []struct {
			Name     string
			Expected struct {
				Status, Reason, RunError string
				OutcomeCauseMessages     []string
				OutcomeAggregate         string
				Models                   []string
				Effects, Receipts        []any
				Counts                   map[string]int
				TransportCalls           int
			}
		}
	}
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	for _, test := range fixtures.Cases {
		t.Run(test.Name, func(t *testing.T) {
			f, base := runtimeFixture(t, Options{})
			a := &runtimeReadyFault{RuntimeAuthority: base, cause: errors.New("synthetic_source_read_failed")}
			scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group", AdapterID: "fixture", AccountID: "bot", PlatformSceneID: "group"}, ParticipationEnabled: true, LearningEnabled: true, ProactiveEnabled: true}
			runtimePut(t, f.main, "scenes", scene.ID, `{"id":"scene","kind":"group","adapterId":"fixture","accountId":"bot","platformSceneId":"group","participationEnabled":true,"learningEnabled":true,"proactiveEnabled":true}`)
			failed := false
			failRead := func() {
				if !failed {
					failed = true
					if _, e := f.main.Exec("DROP TABLE recall_tombstones"); e != nil {
						t.Error(e)
					}
				}
			}
			models := []string{}
			effects := []any{}
			planCalls, transports := 0, 0
			model := botruntime.ModelFunc(func(ctx context.Context, request botruntime.ModelRequest, settle func(botruntime.Completion, error)) {
				if request.Stage == "decision" {
					switch test.Name {
					case "scene-disabled-after-decision":
						_, e := f.main.Exec("UPDATE scenes SET value=json_set(value,'$.participationEnabled',json('false')) WHERE id='scene'")
						if e != nil {
							t.Error(e)
						}
					case "scene-deleted-after-decision":
						_, e := f.main.Exec("DELETE FROM scenes WHERE id='scene'")
						if e != nil {
							t.Error(e)
						}
					case "scene-read-fault-after-decision":
						_, e := f.main.Exec("DROP TABLE scenes")
						if e != nil {
							t.Error(e)
						}
					}
				}
				if test.Name == "decision-read" && request.Stage == "decision" || test.Name == "plan-read" && request.Stage == "plan" {
					failRead()
				}
				if test.Name == "readiness-after-decision" && request.Stage == "decision" {
					a.failed.Store(true)
				}
				models = append(models, request.Stage)
				effects = append(effects, map[string]any{"kind": "model", "stage": request.Stage})
				switch request.Stage {
				case "decision":
					settle(runtimeComplete(runtimeCall("participation.decide", obj("action", "reply", "reasonCode", "fixture", "targetMessageIds", []any{"source"}))), nil)
				case "plan":
					planCalls++
					if test.Name == "tool-read" && planCalls == 1 {
						settle(runtimeComplete(runtimeCall("fixture.read", obj())), nil)
					} else {
						settle(runtimePlan(t, request), nil)
					}
				case "replyer":
					settle(runtimeComplete(runtimeCall("messages.send", obj("operationId", "op", "segments", []any{obj("replyToId", "source", "parts", []any{obj("type", "text", "text", "A concise response.")})}))), nil)
				default:
					settle(botruntime.Completion{}, errors.New("unexpected-model-stage"))
				}
			})
			dates, e := jsdate.New("UTC")
			if e != nil {
				t.Fatal(e)
			}
			compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
			policy, e := replyplan.New(compare, dates)
			if e != nil {
				t.Fatal(e)
			}
			config := botruntime.DefaultSettings()
			config.Persona = "Fixture persona"
			config.PromptContextWindowEnabled = !strings.HasPrefix(test.Name, "scene-")
			config.DebounceMS = 0
			config.MaxWaitMS = 100
			config.GroupCooldownMS = 0
			r, e := botruntime.New(config, botruntime.Services{Authority: a, Models: model, Clock: runtimeTestClock{}, Dates: dates, Policy: policy, Compare: compare})
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			r.AddAdapter("fixture", &botruntime.Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, segment dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
				if test.Name == "before-send" {
					failRead()
				}
				if test.Name == "before-send-once" {
					a.once.Store(true)
				}
				if e := before(); e != nil {
					future := dispatch.NewFuture[dispatch.Receipt]()
					future.Reject(e)
					return future
				}
				transports++
				effects = append(effects, map[string]any{"kind": "send", "segment": jsonDomain(t, segment)})
				id, detail := "sent-1", "delivery_unknown"
				receipt := dispatch.Receipt{Status: dispatch.Sent, MessageID: &id}
				if test.Name == "after-unknown" {
					receipt = dispatch.Receipt{Status: dispatch.Unknown, Detail: &detail}
				}
				effects = append(effects, map[string]any{"kind": "receipt", "receipt": jsonDomain(t, receipt)})
				if test.Name == "after-sent" || test.Name == "after-unknown" {
					failRead()
				}
				return dispatch.Resolved(receipt)
			}}})
			if test.Name == "tool-read" {
				r.AddExtension(&botruntime.Extension{Tools: []*jsjson.Object{obj("name", "learning.propose", "description", "Synthetic learning", "inputSchema", obj("type", "object")), obj("name", "fixture.read", "description", "Synthetic read", "effect", "read", "inputSchema", obj("type", "object"))}, Execute: func(_ context.Context, name string, args any, _ *botruntime.TurnContext, settle func(any, error)) {
					effects = append(effects, map[string]any{"kind": "tool", "tool": name, "args": jsonDomain(t, args)})
					failRead()
					settle(obj("items", []any{"synthetic evidence"}), nil)
				}})
			}
			if e = r.RegisterScene(scene); e != nil {
				t.Fatal(e)
			}
			message := runtimeObject(t, `{"id":"source","sceneId":"scene","senderId":"user","origin":"live","status":"available","sentAt":"2026-10-28T00:00:00.000Z","receivedAt":"2026-10-28T00:00:00.000Z","parts":[{"type":"text","text":"Please respond."}]}`)
			topic, e := r.Ingest(context.Background(), scene.ID, message, false)
			if e != nil {
				t.Fatal(e)
			}
			if test.Name == "initial-read" {
				failRead()
			}
			handle, e := r.Run(scene.ID, topic, false)
			if e != nil {
				t.Fatal(e)
			}
			result := handle.Wait()
			if result.Status != test.Expected.Status || result.Reason != test.Expected.Reason {
				t.Fatalf("outcome %s/%s want %s/%s (error %v)", result.Status, result.Reason, test.Expected.Status, test.Expected.Reason, result.Err)
			}
			if !reflect.DeepEqual(result.Counts, test.Expected.Counts) || !reflect.DeepEqual(models, test.Expected.Models) || transports != test.Expected.TransportCalls {
				t.Fatalf("counts/models/effects %v %v %d", result.Counts, models, transports)
			}
			if !reflect.DeepEqual(jsonDomain(t, effects), test.Expected.Effects) {
				t.Fatalf("effects\n%v\nwant\n%v", jsonDomain(t, effects), test.Expected.Effects)
			}
			if !reflect.DeepEqual(jsonDomain(t, result.Receipts), test.Expected.Receipts) {
				t.Fatalf("receipts %v want %v", jsonDomain(t, result.Receipts), test.Expected.Receipts)
			}
			if result.Err == nil && test.Name != "scene-disabled-after-decision" && test.Name != "scene-deleted-after-decision" {
				t.Fatal("fault lost error")
			}
			if test.Name == "before-send-once" {
				var aggregate *botruntime.OperationOutcomesError
				if !errors.As(result.Err, &aggregate) || !errors.Is(result.Err, a.cause) || aggregate.LastCause() != a.cause {
					t.Fatalf("lost one-shot outcome identity: %v", result.Err)
				}
				if aggregate.Error() != test.Expected.OutcomeAggregate || len(test.Expected.OutcomeCauseMessages) != 1 || test.Expected.OutcomeCauseMessages[0] != a.cause.Error() {
					t.Fatal("oracle outcome identity differs")
				}
				if len(result.Receipts) != 1 || !errors.Is(dispatch.OutcomeError(result.Receipts[0]), a.cause) {
					t.Fatal("receipt lost private error")
				}
			}
			if test.Expected.RunError != "" && (result.CleanupErr == nil || result.CleanupErr.Error() != test.Expected.RunError) {
				t.Fatalf("cleanup error %v", result.Err)
			}
		})
	}
}
