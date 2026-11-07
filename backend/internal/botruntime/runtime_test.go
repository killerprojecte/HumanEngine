package botruntime

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixtureEpoch = 1793145600000.0

// This is deliberately test-only. Production has no permissive Authority.
// Revision identity is independently retained, so A→B→A does not revive a turn.
type testAuthority struct {
	enrolled bool
	raw      string
	mu       sync.Mutex
	records  map[string]*Object
	recalled map[string]bool
	revision map[string]int
	writes   int
	readyErr error
}

func newTestAuthority() *testAuthority {
	return &testAuthority{records: map[string]*Object{}, recalled: map[string]bool{}, revision: map[string]int{}}
}
func (a *testAuthority) SceneParticipationEnabled(s Scene) (bool, error) {
	return s.ParticipationEnabled, nil
}
func (a *testAuthority) Ready() error                                       { a.mu.Lock(); defer a.mu.Unlock(); return a.readyErr }
func (a *testAuthority) RouteIngress(context.Context, Scene, *Object) error { return a.Ready() }
func (a *testAuthority) Observe(_ context.Context, s Scene, m, edit *Object) (string, *Object, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.readyErr != nil {
		return "", nil, a.readyErr
	}
	key := s.ID + "|" + idOf(m)
	if a.recalled[key] {
		return "recalled", nil, nil
	}
	if old := a.records[key]; old != nil && edit == nil {
		return "duplicate", nil, nil
	}
	if edit != nil && a.records[key] == nil {
		return "unorderable", nil, nil
	}
	status := "new"
	if edit != nil {
		status = "edited"
	}
	a.revision[key]++
	copy := cloneJSON(m).(*Object)
	copy.Delete("sourceEventRef")
	if a.enrolled {
		_ = copy.Set("sourceEventRef", obj("eventId", key, "revision", float64(a.revision[key]), "deletionEpoch", float64(0)))
	}
	a.records[key] = copy
	a.writes++
	return status, copy, nil
}
func (a *testAuthority) Recall(_ context.Context, s Scene, id string, prepare func(ReadAuthority) (func() error, error), apply func() error) error {
	callback, err := prepare(a)
	a.mu.Lock()
	a.recalled[s.ID+"|"+id] = true
	a.writes++
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if err = apply(); err != nil {
		return err
	}
	return callback()
}
func (a *testAuthority) ValidateSources(s Scene, rows []*Object, deps []string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.readyErr != nil {
		return false, nil
	}
	for _, id := range deps {
		if a.recalled[s.ID+"|"+id] {
			return false, nil
		}
	}
	for _, m := range rows {
		key := s.ID + "|" + idOf(m)
		if a.recalled[key] {
			return false, nil
		}
		if old := a.records[key]; old != nil {
			oldRef, _ := jsjson.Stringify(get(old, "sourceEventRef"))
			ref, _ := jsjson.Stringify(get(m, "sourceEventRef"))
			if oldRef != ref {
				return false, nil
			}
		}
	}
	return true, nil
}
func (a *testAuthority) IsRecalled(s Scene, id string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.recalled[s.ID+"|"+id], nil
}
func (a *testAuthority) PromptAuthority(s Scene, tools []*Object) (any, error) {
	a.mu.Lock()
	raw := a.raw
	a.mu.Unlock()
	return obj("scope", "test-source-authority:"+s.ID+raw, "tools", objects(tools)), a.Ready()
}
func (a *testAuthority) ProjectDisplay(s Scene, rows []*Object, _ []string) ([]*Object, error) {
	if e := a.Ready(); e != nil {
		return nil, e
	}
	for _, m := range rows {
		if recalled, _ := a.IsRecalled(s, idOf(m)); recalled {
			_ = m.Set("parts", []any{})
			_ = m.Set("status", "recalled")
		}
	}
	return rows, nil
}

type testClock struct {
	mu     sync.Mutex
	now    float64
	timers []*testTimer
}
type testTimer struct {
	clock   *testClock
	at      float64
	f       func()
	stopped bool
}

func (c *testClock) Now() float64 { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) AfterFunc(ms float64, f func()) modelservice.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &testTimer{clock: c, at: c.now + ms, f: f}
	c.timers = append(c.timers, t)
	return t
}
func (t *testTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}
func (c *testClock) Advance(ms float64) {
	c.mu.Lock()
	c.now += ms
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var ready *testTimer
		for _, t := range c.timers {
			if !t.stopped && t.at <= c.now && (ready == nil || t.at < ready.at) {
				ready = t
			}
		}
		if ready != nil {
			ready.stopped = true
		}
		c.mu.Unlock()
		if ready == nil {
			return
		}
		ready.f()
	}
}

func testPolicy(t *testing.T) *replyplan.Policy {
	t.Helper()
	dates, err := jsdate.New("UTC")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := replyplan.New(func(a, b string) (int, error) { return strings.Compare(a, b), nil }, dates)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}
func testRuntime(t *testing.T, model Models) (*Runtime, *testAuthority, *testClock) {
	t.Helper()
	authority := newTestAuthority()
	clock := &testClock{now: fixtureEpoch}
	dates, _ := jsdate.New("UTC")
	cfg := DefaultSettings()
	cfg.Persona = "Fixture persona"
	cfg.DebounceMS = 0
	cfg.MaxWaitMS = 100
	cfg.GroupCooldownMS = 0
	r, err := New(cfg, Services{Models: model, Policy: testPolicy(t), Dates: dates, Authority: authority, Clock: clock, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r, authority, clock
}
func fixtureScene(sim bool) Scene {
	id, kind := "scene", "group"
	if sim {
		id, kind = "sim:fixture", "simulation"
	}
	return Scene{SceneRef: dispatch.SceneRef{ID: id, Kind: kind, AdapterID: "fixture", AccountID: "bot", PlatformSceneID: "group"}, ParticipationEnabled: true, LearningEnabled: true, ProactiveEnabled: true}
}
func fixtureMessage(sceneID, id, text string) *Object {
	return obj("id", id, "sceneId", sceneID, "senderId", "user", "origin", "live", "status", "available", "sentAt", iso(fixtureEpoch), "receivedAt", iso(fixtureEpoch), "parts", []any{obj("type", "text", "text", text)})
}
func completion(calls []replyplan.ToolCall, text string) Completion {
	values := []any{}
	for _, c := range calls {
		values = append(values, obj("id", c.ID, "name", c.Name, "arguments", c.Arguments))
	}
	return Completion{replyplan.Completion{ToolCalls: calls, Assistant: obj("role", "assistant", "content", text, "toolCalls", values)}, text}
}
func toolCall(name string, args any) replyplan.ToolCall {
	return replyplan.ToolCall{ID: name, Name: name, Arguments: args}
}
func fixtureDecision(action string) Completion {
	return completion([]replyplan.ToolCall{toolCall("participation.decide", obj("action", action, "reasonCode", "fixture", "targetMessageIds", []any{"source"}))}, "")
}
func requestData(req ModelRequest) *Object {
	data := obj()
	for _, m := range req.Messages {
		if get(m, "role") == "assistant" || get(m, "role") == "tool" {
			break
		}
		if get(m, "role") == "user" {
			v, err := jsjson.Parse(str(get(m, "content")))
			if err != nil {
				panic(err)
			}
			for _, f := range v.(*Object).Members() {
				_ = data.Set(f.Key, f.Value)
			}
		}
	}
	return data
}
func fixturePlan(req ModelRequest, action string, actions []any) Completion {
	contract := get(requestData(req), "planContract")
	return completion([]replyplan.ToolCall{toolCall("reply.plan", obj("action", action, "deliveryTool", "messages.send", "targetMessageIds", get(contract, "targetMessageIds"), "contextMessageIds", get(contract, "contextMessageIds"), "strategy", "Answer the selected fixture context.", "constraints", []any{}, "facts", []any{}, "assets", []any{}, "emoji", []any{}, "memorySelections", []any{}, "expressionGoals", []any{"Compose a concise fixture reply."}, "actions", actions))}, "")
}
func fixtureSend(text string, second bool) replyplan.ToolCall {
	segments := []any{obj("replyToId", "source", "parts", []any{obj("type", "text", "text", text)})}
	if second {
		segments = append(segments, obj("replyToId", "source", "parts", []any{obj("type", "text", "text", "A second segment.")}))
	}
	return toolCall("messages.send", obj("operationId", "op", "segments", segments))
}
func decoded(v any) any {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	var x any
	if e = json.Unmarshal(b, &x); e != nil {
		panic(e)
	}
	return x
}
func TestActualRuntimeTurnOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/runtime.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name     string
			Expected map[string]any
			Requests []any
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixture.Cases {
		t.Run(row.Name, func(t *testing.T) { runRuntimeOracle(t, row.Name, row.Expected, row.Requests, testPolicy(t)) })
	}
}
func runRuntimeOracle(t *testing.T, name string, expected map[string]any, expectedRequests []any, policy *replyplan.Policy) {
	t.Helper()
	var runtime *Runtime
	var authority *testAuthority
	models := []string{}
	effects := []any{}
	warnings := []string{}
	modelRequests := []any{}
	actions, reads, sent := []any{}, []string{}, []dispatch.Segment{}
	transportCalls, planCalls := 0, 0
	scene := fixtureScene(strings.HasPrefix(name, "simulation"))
	model := ModelFunc(func(ctx context.Context, req ModelRequest, settle func(Completion, error)) {
		models = append(models, req.Stage)
		effects = append(effects, map[string]any{"kind": "model", "stage": req.Stage})
		modelRequests = append(modelRequests, obj("stage", req.Stage, "role", req.Role, "messages", cloneJSON(objects(req.Messages)), "tools", cloneJSON(objects(req.Tools))))
		switch req.Stage {
		case "decision":
			if strings.HasPrefix(name, "simulation") && name != "simulation" {
				authority.mu.Lock()
				if name == "simulationRecoveryOff" {
					authority.readyErr = code("recovery_gate_invalid")
				} else {
					authority.raw = name
				}
				authority.mu.Unlock()
			}
			if name == "recallDecision" {
				if e := runtime.Recall(ctx, scene.ID, "source"); e != nil {
					t.Error(e)
				}
			}
			decision := "reply"
			if name == "silent" {
				decision = "silent"
			}
			if name == "wait" {
				decision = "wait"
			}
			out := fixtureDecision(decision)
			if name == "reasonOverLimit" || name == "reasonLimitInvalid" {
				_ = out.ToolCalls[0].Arguments.(*Object).Set("reasonCode", strings.Repeat("x", 201))
			}
			if name == "reasonAstralFits" {
				_ = out.ToolCalls[0].Arguments.(*Object).Set("reasonCode", strings.Repeat("😀", 101))
			}
			if name == "reasonLimitInvalid" {
				targets := []any{}
				for i := 0; i < 21; i++ {
					targets = append(targets, "source")
				}
				_ = out.ToolCalls[0].Arguments.(*Object).Set("targetMessageIds", targets)
			}
			if decision == "wait" {
				_ = out.ToolCalls[0].Arguments.(*Object).Set("waitForMs", float64(50))
			}
			settle(out, nil)
		case "validation":
			settle(completion([]replyplan.ToolCall{toolCall("reply.validate", obj("valid", false))}, ""), nil)
		case "plan":
			planCalls++
			if name == "missingPlan" {
				settle(completion(nil, ""), nil)
				return
			}
			if (name == "read" || name == "readFailed") && planCalls == 1 {
				settle(completion([]replyplan.ToolCall{toolCall("fixture.read", obj())}, ""), nil)
				return
			}
			if name == "staleValidation" {
				_, e := runtime.Ingest(ctx, scene.ID, fixtureMessage(scene.ID, "correction", "Correction: do not send that answer."), false)
				if e != nil {
					t.Error(e)
				}
			}
			action := "reply"
			if name == "noReply" || name == "noReplyAction" {
				action = "noReply"
			}
			plannedActions := []any{}
			if contains([]string{"actionQueued", "actionFailed", "noReplyAction"}, name) {
				plannedActions = append(plannedActions, obj("toolName", "learning.propose", "arguments", obj("fixture", true)))
			}
			settle(fixturePlan(req, action, plannedActions), nil)
		case "replyer":
			if name == "terminalText" {
				settle(completion(nil, "This must not be delivered."), nil)
				return
			}
			if name == "forbiddenTool" {
				settle(completion([]replyplan.ToolCall{toolCall("fixture.forbidden", obj())}, ""), nil)
				return
			}
			if name == "recallReplyer" {
				_ = runtime.Recall(ctx, scene.ID, "source")
			}
			if name == "cancelReplyer" {
				runtime.Cancel(scene.ID)
			}
			if name == "disposeReplyer" {
				runtime.Close()
			}
			text := "A concise response."
			if name == "grounding" {
				text = "I will send you another image tomorrow."
			}
			send := fixtureSend(text, contains([]string{"reply", "simulation", "partial"}, name))
			calls := []replyplan.ToolCall{send}
			if name == "twoCalls" {
				second := fixtureSend(text, false)
				_ = second.Arguments.(*Object).Set("operationId", "op2")
				second.ID = "send2"
				calls = append(calls, second)
			}
			settle(completion(calls, ""), nil)
		default:
			t.Errorf("unexpected stage %s", req.Stage)
			settle(Completion{}, code("unexpected_stage"))
		}
	})
	r, a, _ := testRuntime(t, model)
	runtime = r
	authority = a
	if name == "simulationRecoveryOff" {
		cfg := r.config()
		cfg.PromptContextWindowEnabled = false
		_ = r.Configure(cfg)
	}
	runtime.services.Policy = policy
	runtime.services.Trace = func(event Event) {
		if event.Level == "warn" && event.Module == "runtime" {
			warnings = append(warnings, event.Code)
		}
	}
	if name == "suggestions" {
		cfg := runtime.config()
		cfg.ReplySuggestionsEnabled = true
		_ = runtime.Configure(cfg)
	}
	if err := r.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, segment dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		if e := before(); e != nil {
			return dispatch.Rejected[dispatch.Receipt](e)
		}
		transportCalls++
		sent = append(sent, segment)
		effects = append(effects, map[string]any{"kind": "send", "segment": segment})
		status := dispatch.Sent
		detail := ""
		if name == "unknown" {
			status = dispatch.Unknown
			detail = "delivery_unknown"
		}
		if name == "failed" || name == "partial" && transportCalls == 2 {
			status = dispatch.Failed
			detail = "fixture_failed"
		}
		receipt := dispatch.Receipt{Status: status}
		if status == dispatch.Sent {
			receipt.MessageID = ptr("sent-" + string(rune('0'+transportCalls)))
		} else {
			receipt.Detail = &detail
		}
		effects = append(effects, map[string]any{"kind": "receipt", "receipt": receipt})
		return dispatch.Resolved(receipt)
	}}})
	if contains([]string{"actionQueued", "actionFailed", "noReplyAction", "read", "readFailed"}, name) {
		r.AddExtension(&Extension{Tools: []*Object{obj("name", "learning.propose", "description", "Synthetic learning", "inputSchema", obj("type", "object")), obj("name", "fixture.read", "description", "Synthetic read", "effect", "read", "inputSchema", obj("type", "object"))}, Execute: func(_ context.Context, tool string, args any, _ *TurnContext, done func(any, error)) {
			effects = append(effects, map[string]any{"kind": "tool", "tool": tool, "args": json.RawMessage(mustJSON(args))})
			if tool == "fixture.read" {
				reads = append(reads, tool)
				if name == "readFailed" {
					done(nil, code("fixture_read_failed"))
					return
				}
				done(obj("items", []any{"synthetic evidence"}), nil)
				return
			}
			actions = append(actions, map[string]any{"tool": tool, "args": map[string]any{"fixture": true}})
			if name == "actionFailed" {
				done(nil, code("fixture_action_failed"))
				return
			}
			done(obj("status", "queued", "jobId", "fixture-job"), nil)
		}})
	}
	m := fixtureMessage(scene.ID, "source", "Please respond.")
	if scene.Kind == "simulation" {
		_ = m.Set("origin", "simulation")
	}
	topic, err := r.Ingest(context.Background(), scene.ID, m, false)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := r.Run(scene.ID, topic, false)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-handle.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("turn blocked")
	}
	result := handle.Wait()
	snapshot, snapshotErr := r.Snapshot(scene.ID)
	if state := r.state(scene.ID); state != nil && snapshotErr != nil {
		view := state.view.Load()
		snapshot = Snapshot{Messages: bodyRows(view.history), Active: view.active}
		snapshotErr = nil
	}
	bots := []any{}
	var sourceStatus any
	for _, row := range snapshot.Messages {
		if get(row, "senderId") == "bot" {
			parts := []any{}
			for _, p := range array(get(row, "parts")) {
				text := str(get(p, "text"))
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
			bots = append(bots, map[string]any{"parts": parts, "replyToId": get(row, "replyToId")})
		}
		if idOf(row) == "source" {
			sourceStatus = get(row, "status")
		}
	}
	actual := map[string]any{"effects": effects, "warnings": warnings, "models": models, "actions": actions, "reads": reads, "sent": sent, "transportCalls": transportCalls, "botMessages": bots, "sourceStatus": sourceStatus, "status": result.Status, "reason": result.Reason, "counts": result.Counts, "active": snapshot.Active, "registered": snapshotErr == nil, "persistentConfigDelta": 0}
	if !reflect.DeepEqual(decoded(actual), expected) {
		a, _ := json.MarshalIndent(actual, "", "  ")
		b, _ := json.MarshalIndent(expected, "", "  ")
		t.Errorf("actual source trace mismatch\nactual=%s\nexpected=%s\nerror=%v", a, b, result.Err)
	}
	if expectedRequests != nil {
		normalized := normalizeRequests(modelRequests, topic)
		text, _ := jsjson.Stringify(normalized)
		var got []any
		if e := json.Unmarshal([]byte(text), &got); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(got, expectedRequests) {
			actual, _ := json.MarshalIndent(got, "", "  ")
			want, _ := json.MarshalIndent(expectedRequests, "", "  ")
			t.Errorf("full model requests mismatch\nactual=%s\nexpected=%s", actual, want)
		}
	}
	if scene.Kind == "simulation" && a.writes != 0 {
		t.Fatalf("simulation touched real authority %d times", a.writes)
	}
}

func normalizeRequests(value any, topicID string) any {
	switch v := value.(type) {
	case []any:
		out := []any{}
		for _, x := range v {
			out = append(out, normalizeRequests(x, topicID))
		}
		return out
	case *Object:
		out := obj()
		for _, m := range v.Members() {
			value := m.Value
			if m.Key == "content" {
				if text, ok := value.(string); ok && strings.HasPrefix(text, "{") {
					data, err := jsjson.Parse(text)
					if err != nil {
						panic(err)
					}
					text, err = jsjson.Stringify(normalizeRequests(data, topicID))
					if err != nil {
						panic(err)
					}
					_ = out.Set(m.Key, text)
					continue
				}
			}
			_ = out.Set(m.Key, normalizeRequests(value, topicID))
		}
		return out
	case string:
		if v == topicID {
			return "fixture-topic"
		}
	}
	return value
}

func mustJSON(v any) []byte {
	s, err := jsjson.Marshal(v)
	if err != nil {
		panic(err)
	}
	return s
}
