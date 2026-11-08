package experienceservice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"sync"
	"testing"
	"time"
)

type runtimeFixtureClock struct{}

func (runtimeFixtureClock) Now() float64 { return float64(fixtureNow) }
func (runtimeFixtureClock) AfterFunc(ms float64, fn func()) modelservice.Timer {
	return time.AfterFunc(time.Duration(ms*float64(time.Millisecond)), fn)
}
func runtimeCompletion(name string, args any) botruntime.Completion {
	call := replyplan.ToolCall{ID: name, Name: name, Arguments: args}
	return botruntime.Completion{Completion: replyplan.Completion{ToolCalls: []replyplan.ToolCall{call}, Assistant: object("role", "assistant", "content", "", "toolCalls", []any{object("id", name, "name", name, "arguments", args)})}}
}
func runtimeRequest(req botruntime.ModelRequest) *jsjson.Object {
	out := object()
	for _, m := range req.Messages {
		role := prop(m, "role")
		if role == "assistant" || role == "tool" {
			break
		}
		if role == "user" {
			v, e := jsjson.Parse(stringValue(prop(m, "content")))
			if e != nil {
				panic(e)
			}
			for _, k := range v.(*jsjson.Object).Keys() {
				_ = out.Set(k, prop(v.(*jsjson.Object), k))
			}
		}
	}
	return out
}
func runtimeMessage(id, sender, text, reply string) *jsjson.Object {
	v := object("id", id, "sceneId", "scene", "senderId", sender, "origin", "live", "status", "available", "sentAt", isoMillis(fixtureNow), "receivedAt", isoMillis(fixtureNow), "parts", []any{object("type", "text", "text", text)})
	if reply != "" {
		_ = v.Set("replyToId", reply)
	}
	return v
}
func runtimeTool(name, effect string) *jsjson.Object {
	return object("name", name, "description", "Synthetic fixture tool", "effect", effect, "inputSchema", object("type", "object"))
}
func TestRuntimeHintsNativeEndToEnd(t *testing.T) {
	f := newLiveFixture(t, true)
	ctx := context.Background()
	dates, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	policy, e := replyplan.New(f.service.ports.Compare, dates)
	if e != nil {
		t.Fatal(e)
	}
	authority, e := sourceevents.NewRuntimeAuthority(f.events)
	if e != nil {
		t.Fatal(e)
	}

	settings, e := jsjson.Parse(string(contract.DefaultSettingsInput()))
	if e != nil {
		t.Fatal(e)
	}
	settingsObject := settings.(*jsjson.Object)
	for _, key := range []string{"memoryExperienceEnabled", "memoryExperienceAutoActivateEnabled", "memoryExperienceHintsEnabled", "memoryExperienceFeedbackEnabled"} {
		_ = settingsObject.Set(key, true)
	}
	settingsJSON, e := jsjson.Stringify(settingsObject)
	if e != nil {
		t.Fatal(e)
	}
	mustExec(t, f.main, "UPDATE config SET value=? WHERE key='settings'", settingsJSON)
	// Real scene persistence includes participation, not a permissive read stub.
	mustExec(t, f.main, "UPDATE scenes SET value=? WHERE id='scene'", `{"id":"scene","kind":"group","learningEnabled":true,"participationEnabled":true,"adapterId":"synthetic","accountId":"account","platformSceneId":"scene","name":"Synthetic","proactiveEnabled":false}`)
	cfg := botruntime.DefaultSettings()
	cfg.Persona = "Synthetic runtime test"
	cfg.CacheEnabled = true
	cfg.CacheHours = 24
	cfg.DebounceMS = 0
	cfg.GroupCooldownMS = 0
	var modelMu sync.Mutex
	plans := 0
	var captured *botruntime.TurnContext
	readDone := make(chan *botruntime.TurnContext, 1)
	resume := make(chan struct{})
	models := botruntime.ModelFunc(func(_ context.Context, req botruntime.ModelRequest, settle func(botruntime.Completion, error)) {
		switch req.Stage {
		case "decision":
			settle(runtimeCompletion("participation.decide", object("action", "reply", "reasonCode", "fixture", "targetMessageIds", []any{"source"})), nil)
		case "plan":
			modelMu.Lock()
			plans++
			n := plans
			captured = req.Turn
			modelMu.Unlock()
			if n == 1 {
				settle(runtimeCompletion("experience.outcomes", object()), nil)
				return
			}
			if n == 2 {
				readDone <- req.Turn
				go func() { <-resume; settle(runtimeCompletion("memory.person", object("personId", "alice")), nil) }()
				return
			}
			contract, _ := prop(runtimeRequest(req), "planContract").(*jsjson.Object)
			settle(runtimeCompletion("reply.plan", object("action", "reply", "deliveryTool", "messages.send", "targetMessageIds", prop(contract, "targetMessageIds"), "contextMessageIds", prop(contract, "contextMessageIds"), "strategy", "Answer the selected context.", "constraints", []any{}, "facts", []any{}, "assets", []any{}, "emoji", []any{}, "memorySelections", []any{}, "expressionGoals", []any{"Compose a short reply."}, "actions", []any{})), nil)
		case "replyer":
			settle(runtimeCompletion("messages.send", object("operationId", "op", "segments", []any{object("replyToId", "source", "parts", []any{object("type", "text", "text", "Synthetic answer.")})})), nil)
		default:
			settle(botruntime.Completion{}, fmt.Errorf("unexpected stage %s", req.Stage))
		}
	})
	runtime, e := botruntime.New(cfg, botruntime.Services{Models: models, Policy: policy, Dates: dates, Authority: authority, Clock: runtimeFixtureClock{}, Compare: f.service.ports.Compare})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(runtime.Close)
	ports, e := NativeRuntimePorts(runtime, f.events, f.service.ports.Compare)
	if e != nil {
		t.Fatal(e)
	}
	ports.Now = f.service.ports.Now
	ports.ID = f.service.ports.ID
	service, e := New(ctx, f.main, f.authority, ports)
	if e != nil {
		t.Fatal(e)
	}
	hints, e := NewRuntimeHints(service, f.events, runtime.PreviewMessage)
	if e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	received, delivered, gateCalls, plannerCalls := 0, 0, 0, 0
	var reports []error
	baseSentEntered, releaseBaseSent := make(chan struct{}), make(chan struct{})
	var releaseBaseOnce sync.Once
	releaseBase := func() { releaseBaseOnce.Do(func() { close(releaseBaseSent) }) }
	defer releaseBase()
	base := botruntime.Extension{Sent: func(botruntime.Scene, string, string) { close(baseSentEntered); <-releaseBaseSent }, Tools: []*jsjson.Object{runtimeTool("memory.person", "read")}, Received: func(*jsjson.Object, botruntime.Scene) error { mu.Lock(); received++; mu.Unlock(); return nil }, PlannerContextData: func(*botruntime.TurnContext, []*jsjson.Object) (any, error) { return object("base", "retained"), nil }, Execute: func(_ context.Context, name string, args any, c *botruntime.TurnContext, done func(any, error)) {
		if name != "memory.person" {
			done(nil, errors.New("unexpected_tool"))
			return
		}
		done([]any{object("text", "Synthetic fact")}, nil)
	}, Delivered: func(name string, v any, c *botruntime.TurnContext, args any) (any, error) {
		mu.Lock()
		delivered++
		mu.Unlock()
		return v, nil
	}}
	gates := RuntimeGates{Tools: []*jsjson.Object{runtimeTool("experience.outcomes", "read"), runtimeTool("experience.propose", "write")}, Tool: func(string, *botruntime.TurnContext) error { mu.Lock(); gateCalls++; mu.Unlock(); return nil }, Proposal: func(*botruntime.TurnContext) (string, error) { return "semantic_read_not_learning_authority", nil }, Planner: func(c *botruntime.TurnContext, run func() (any, error)) (any, error) {
		mu.Lock()
		plannerCalls++
		mu.Unlock()
		return run()
	}, Report: func(_ string, e error) { mu.Lock(); reports = append(reports, e); mu.Unlock() }}
	ext, _, e := hints.ComposeExtension(base, gates)
	if e != nil {
		t.Fatal(e)
	}
	remove := runtime.AddExtension(ext)
	t.Cleanup(remove)

	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{ID: "scene", Kind: "group", AdapterID: "synthetic", AccountID: "account", PlatformSceneID: "scene"}, ParticipationEnabled: true, LearningEnabled: true}
	if e = runtime.RegisterScene(scene); e != nil {
		t.Fatal(e)
	}
	runtime.AddAdapter("synthetic", &botruntime.Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		if e := before(); e != nil {
			return dispatch.Rejected[dispatch.Receipt](e)
		}
		return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr("sent")})
	}}})
	if _, e = runtime.Ingest(ctx, "scene", runtimeMessage("intro", "bot", "Synthetic prior reply.", ""), false); e != nil {
		t.Fatal(e)
	}
	if _, e = runtime.Ingest(ctx, "scene", runtimeMessage("instruction", "alice", "涉及我的问题请先查我的记忆", "intro"), false); e != nil {
		t.Fatal(e)
	}
	outcomes, e := service.ListOutcomes(ctx, testScope, 20)
	if e != nil || len(outcomes) != 1 {
		t.Fatalf("received feedback %v %v", outcomes, e)
	}
	topic, e := runtime.Ingest(ctx, "scene", runtimeMessage("source", "alice", "我是谁？", ""), false)
	if e != nil {
		t.Fatal(e)
	}
	handle, e := runtime.Run("scene", topic, false)
	if e != nil {
		t.Fatal(e)
	}
	var turn *botruntime.TurnContext
	select {
	case turn = <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not expose outcomes")
	}
	// Model is pending; one real writer can validate current hints and exposure
	// using its own scoped read capability without reentering the writer.
	e = f.authority.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		return f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			valid, e := turn.ValidateMemoryContextWith(reads)
			if e != nil {
				return e
			}
			if !valid {
				return errors.New("expected_scoped_validation")
			}
			return nil
		})
	})
	if e != nil {
		close(resume)
		t.Fatal(e)
	}

	// Pending output identity is not recreated by an equivalent JSON object.
	pending, e := hints.Outcomes(turn)
	if e != nil || len(pending) != 1 {
		close(resume)
		t.Fatalf("outcomes %v %v", pending, e)
	}
	copied := cloneReferenceValue(pending[0])
	if got, e := hints.DeliverOutcomes(turn, []any{copied}); e != nil || len(got) != 0 {
		close(resume)
		t.Fatalf("copied identity accepted %v %v", got, e)
	}
	original := pending[0].(*jsjson.Object)
	oldCode := prop(original, "code")
	_ = original.Set("code", "forged")
	if got, e := hints.DeliverOutcomes(turn, []any{original}); e != nil || len(got) != 0 {
		close(resume)
		t.Fatalf("mutated view accepted %v %v", got, e)
	}
	_ = original.Set("code", oldCode)
	proposed, e := hints.Propose(turn, object("action", "exact_person_before_semantic", "condition", object("kind", "person_question", "personId", "alice"), "outcomeRefs", []any{object("id", outcomes[0].ID, "version", float64(outcomes[0].Version))}))
	if e != nil || prop(proposed.(*jsjson.Object), "duplicate") != true {
		close(resume)
		t.Fatalf("scoped proposal %v %v", proposed, e)
	}
	// Existing semantic proposal gate must win before any Experience write.
	var rejected any
	ext.Execute(ctx, "experience.propose", object(), turn, func(v any, e error) {
		if e != nil {
			t.Error(e)
		}
		rejected = v
	})
	if prop(rejected.(*jsjson.Object), "reason") != "semantic_read_not_learning_authority" {
		t.Fatal(rejected)
	}
	close(resume)
	// ACK/body is visible while the inherited callback remains blocked. Do
	// not wait for handle completion before starting the external reply.
	select {
	case <-baseSentEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("base Sent not entered")
	}
	ackSnapshot, e := runtime.Snapshot("scene")
	if e != nil {
		t.Fatal(e)
	}
	ackVisible := false
	for _, m := range ackSnapshot.Messages {
		if prop(m, "id") == "sent" {
			ackVisible = true
		}
	}
	if !ackVisible {
		t.Fatal("sent body not published before base callback")
	}
	replyDone := make(chan error, 1)
	go func() {
		_, e := runtime.Ingest(ctx, "scene", runtimeMessage("immediate-help", "alice", "这次先查我的记忆有帮助", "sent"), false)
		replyDone <- e
	}()
	// Wait until actual ingress published its retained row, before its Received
	// callback. No test-side Flush is used to establish association ordering.
	deadline := time.After(3 * time.Second)
	for {
		snapshot, e := runtime.Snapshot("scene")
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, m := range snapshot.Messages {
			if prop(m, "id") == "immediate-help" {
				found = true
			}
		}
		if found {
			break
		}
		select {
		case <-deadline:
			t.Fatal("reply ingress not published")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case e := <-replyDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reply association barrier did not finish while base Sent remained blocked")
	}
	releaseBase()
	result := handle.Wait()
	learned, e := service.ListOutcomes(ctx, testScope, 20)
	if e != nil {
		t.Fatal(e)
	}
	helpful := false
	for _, o := range learned {
		if o.Source.MessageID == "immediate-help" && o.Code == "user_reported_helpful" && o.PolicyRef != nil {
			helpful = true
		}
	}
	if !helpful {
		t.Fatalf("immediate reply lost policy attribution: %+v", learned)
	}

	if result.Err != nil {
		t.Fatal(result.Err)
	}
	uses, e := service.TargetUses(ctx, testScope, "sent")
	if e != nil {
		t.Fatal(e)
	}
	kinds := map[string]bool{}
	for _, u := range uses {
		kinds[u.Kind+":"+u.Operation] = true
	}
	if !kinds["exposed:"] || !kinds["applied:memory_person"] {
		t.Fatalf("sent associations: %+v result %+v", uses, result)
	}
	modelMu.Lock()
	if captured == nil {
		t.Fatal("no runtime turn")
	}
	modelMu.Unlock()
	// Source Runtime ingress returns before Received for bot messages. The three
	// callbacks are instruction, question and immediate feedback, not the bot reply.
	mu.Lock()
	if received != 3 || delivered != 1 || gateCalls < 2 || plannerCalls != 1 || len(reports) > 0 {
		t.Fatalf("callbacks received=%d delivered=%d gates=%d planner=%d reports=%v", received, delivered, gateCalls, plannerCalls, reports)
	}
	mu.Unlock()
	scene.LearningEnabled = false
	if e = runtime.RegisterScene(scene); e != nil {
		t.Fatal(e)
	}
	if hints.epoch("scene") != 1 {
		t.Fatal("learning pause not wired")
	}
}
func TestRuntimeReadViewSameOwnerExpiryAndRecalled(t *testing.T) {
	f := newLiveFixture(t, false)
	p := admitted(t, f)
	other := newLiveFixture(t, false)
	ctx := context.Background()
	if _, e := NewRuntimeHints(f.service, other.events, func(string, string, botruntime.ReadAuthority) (*jsjson.Object, error) { return nil, nil }); e == nil {
		t.Fatal("foreign runtime composition accepted before exposure")
	}
	var retained *RuntimeView
	e := f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		return f.events.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			if e := other.service.WithRuntimeView(scope, func(*RuntimeView) error { return nil }); e == nil {
				t.Fatal("foreign service accepted")
			}
			return f.service.WithRuntimeView(scope, func(v *RuntimeView) error {
				retained = v
				valid, e := v.SnapshotValid(testScope, Snapshot{Reference: Reference{ID: p.ID, Version: p.Version}, Hash: p.Hash})
				if e != nil {
					return e
				}
				if !valid {
					t.Fatal("live read invalid")
				}
				return nil
			})
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = retained.ListOutcomes(testScope, 20); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("retained view survived", e)
	}
	e = f.authority.WithWriteSession(ctx, f.main, func(ws *recovery.WriteSession) error {
		if _, e := ws.Exec("INSERT INTO recall_tombstones VALUES('scene','instruction','now')"); e != nil {
			return e
		}
		return f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			return f.events.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
				return f.service.WithRuntimeView(scope, func(v *RuntimeView) error {
					rows, e := v.ListOutcomes(testScope, 20)
					if e != nil {
						return e
					}
					if len(rows) != 0 {
						t.Fatal("ignored uncommitted recall")
					}
					return nil
				})
			})
		})
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestRuntimeReadViewMatchesWriterTrialQualification(t *testing.T) {
	f := newLiveFixture(t, true)
	ctx := context.Background()
	admitted, e := f.service.RecordFeedback(ctx, testScope, feedback("request_exact_person_lookup"))
	if e != nil || admitted == nil || admitted.Candidate == nil {
		t.Fatalf("trial admission %+v %v", admitted, e)
	}
	hints, e := f.service.Hints(ctx, testScope, HintQuery{PersonID: "alice", RequesterID: "alice", PersonQuestion: true})
	if e != nil || len(hints) != 1 {
		t.Fatalf("trial hints %+v %v", hints, e)
	}
	ref := hintSnapshot(hints[0])
	for _, step := range []struct {
		name, query string
		args        []any
	}{
		{"live", "", nil},
		{"paused", "UPDATE scenes SET value=json_set(value,'$.learningEnabled',false) WHERE id='scene'", nil},
		{"resumed", "UPDATE scenes SET value=json_set(value,'$.learningEnabled',json('true')) WHERE id='scene'", nil},
		{"changed-revision", "UPDATE config SET value='2' WHERE key='memory-experience-feedback-revision'", nil},
		{"restored-revision", "UPDATE config SET value='1' WHERE key='memory-experience-feedback-revision'", nil},
		{"recalled-source", "INSERT INTO recall_tombstones VALUES('scene','instruction','now')", nil},
	} {
		t.Run(step.name, func(t *testing.T) {
			e := f.service.TransactionWithSession(ctx, func(bound *Service, ws *recovery.WriteSession) error {
				if step.query != "" {
					if _, e := ws.Exec(step.query, step.args...); e != nil {
						return e
					}
				}
				want, e := bound.SnapshotValid(ctx, testScope, ref)
				if e != nil {
					return e
				}
				outcomes, e := bound.ListOutcomes(ctx, testScope, 20)
				if e != nil {
					return e
				}
				return f.events.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
					return f.events.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
						return f.service.WithRuntimeView(scope, func(view *RuntimeView) error {
							got, e := view.SnapshotValid(testScope, ref)
							if e != nil {
								return e
							}
							if got != want {
								t.Fatalf("read=%v writer=%v", got, want)
							}
							rows, e := view.ListOutcomes(testScope, 20)
							if e != nil {
								return e
							}
							if encoded(rows) != encoded(outcomes) {
								t.Fatalf("outcomes read=%s writer=%s", encoded(rows), encoded(outcomes))
							}
							return nil
						})
					})
				})
			})
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}
