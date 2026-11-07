package botruntime

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func awaitTurn(t *testing.T, h *TurnHandle) TurnResult {
	t.Helper()
	select {
	case <-h.Done:
		return h.Wait()
	case <-time.After(2 * time.Second):
		t.Fatal("turn failed to release after cancellation")
		return TurnResult{}
	}
}
func awaitCondition(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for !f() {
		select {
		case <-deadline:
			t.Fatal("condition did not settle")
		case <-time.After(time.Millisecond):
		}
	}
}
func registerFixture(t *testing.T, r *Runtime) string {
	t.Helper()
	scene := fixtureScene(false)
	if e := r.RegisterScene(scene); e != nil {
		t.Fatal(e)
	}
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		if e := before(); e != nil {
			return dispatch.Rejected[dispatch.Receipt](e)
		}
		return dispatch.Resolved(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr("confirmed")})
	}}})
	topic, e := r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "source", "Please respond."), false)
	if e != nil {
		t.Fatal(e)
	}
	return topic
}
func TestQueriesIngressRecallAndLateModelAreIndependent(t *testing.T) {
	for _, stage := range []string{"decision", "plan", "replyer"} {
		t.Run(stage, func(t *testing.T) {
			started := make(chan ModelRequest, 1)
			release := make(chan func(Completion, error), 1)
			var calls atomic.Int64
			r, a, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
				calls.Add(1)
				if req.Stage == stage {
					started <- req
					release <- done
					return
				}
				if req.Stage == "decision" {
					done(fixtureDecision("reply"), nil)
				} else {
					done(fixturePlan(req, "reply", nil), nil)
				}
			}))
			topic := registerFixture(t, r)
			h, e := r.Run("scene", topic, false)
			if e != nil {
				t.Fatal(e)
			}
			req := <-started
			before := req.Turn.Sources()
			query := make(chan Snapshot, 1)
			go func() {
				snapshot, e := r.Snapshot("scene")
				if e != nil {
					t.Error(e)
				}
				query <- snapshot
			}()
			select {
			case snapshot := <-query:
				if snapshot.Active != 1 || len(snapshot.Messages) != 1 {
					t.Fatal(snapshot)
				}
			case <-time.After(time.Second):
				t.Fatal("query queued behind model")
			}
			if _, e := r.Ingest(context.Background(), "scene", fixtureMessage("scene", "fresh", "Fresh unrelated input."), false); e != nil {
				t.Fatal(e)
			}
			if e := r.Recall(context.Background(), "scene", "source"); e != nil {
				t.Fatal(e)
			}
			result := awaitTurn(t, h)
			if result.Status != "cancelled" {
				t.Fatalf("result=%+v", result)
			}
			if len(before) != 1 || get(before[0], "status") != "available" || len(array(get(before[0], "parts"))) == 0 {
				t.Fatal("snapshot mutated in place")
			}
			settle := <-release
			settle(completion([]replyplan.ToolCall{fixtureSend("late", false)}, ""), nil)
			snapshot, e := r.Snapshot("scene")
			if e != nil {
				t.Fatal(e)
			}
			if snapshot.Active != 0 || len(snapshot.Messages) != 2 || get(snapshot.Messages[0], "status") != "recalled" {
				t.Fatalf("late model resurrected state: %+v", snapshot)
			}
			if recalled, _ := a.IsRecalled(snapshot.Scene, "source"); !recalled {
				t.Fatal("durable source tombstone missing")
			}
		})
	}
}
func TestOrderedEditsNeverUpgradeOldSnapshot(t *testing.T) {
	started := make(chan *TurnContext, 1)
	release := make(chan func(Completion, error), 1)
	r, a, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		if req.Stage == "decision" {
			done(fixtureDecision("reply"), nil)
			return
		}
		if req.Stage == "plan" {
			done(fixturePlan(req, "reply", nil), nil)
			return
		}
		started <- req.Turn
		release <- done
	}))
	a.enrolled = true
	topic := registerFixture(t, r)
	h, _ := r.Run("scene", topic, false)
	turn := <-started
	for i, body := range []string{"B", "Please respond."} {
		status, e := r.Edit(context.Background(), "scene", fixtureMessage("scene", "source", body), obj("editId", string(rune('a'+i)), "order", obj("kind", "platform_sequence", "sequence", float64(i+1))), false)
		if e != nil || status != "edited" {
			t.Fatalf("edit: %s %v", status, e)
		}
	}
	result := awaitTurn(t, h)
	if result.Status != "cancelled" {
		t.Fatal(result)
	}
	if get(get(turn.Sources()[0], "sourceEventRef"), "revision") != float64(1) {
		t.Fatal("old snapshot upgraded")
	}
	snapshot, _ := r.Snapshot("scene")
	if get(get(snapshot.Messages[0], "sourceEventRef"), "revision") != float64(3) {
		t.Fatal("new revision missing")
	}
	(<-release)(completion([]replyplan.ToolCall{fixtureSend("must not send", false)}, ""), nil)
	if len(r.outcomes(turn.owner)) != 0 {
		t.Fatal("old body replay sent")
	}
}
func TestWaitExpiresOnlyAsCheapRecheck(t *testing.T) {
	var calls, rechecks atomic.Int64
	cleaned := make(chan struct{}, 1)
	r, _, clock := testRuntime(t, ModelFunc(func(_ context.Context, _ ModelRequest, done func(Completion, error)) {
		calls.Add(1)
		out := fixtureDecision("wait")
		_ = out.ToolCalls[0].Arguments.(*Object).Set("waitForMs", float64(50))
		done(out, nil)
	}))
	r.services.Trace = func(e Event) {
		if e.Stage == "turn_cleanup" && e.Status == "completed" {
			cleaned <- struct{}{}
		}
		if e.Stage == "turn_wait" {
			rechecks.Add(1)
		}
	}
	registerFixture(t, r)
	_, e := r.Ingest(context.Background(), "scene", fixtureMessage("scene", "scheduled", "Fresh input"), true)
	if e != nil {
		t.Fatal(e)
	}
	clock.Advance(0)
	// Owner removal publishes active=0 before cleanup installs the wait timer.
	// Wait for actual cleanup completion before advancing the synchronous fake clock.
	select {
	case <-cleaned:
	case <-time.After(2 * time.Second):
		t.Fatal("wait cleanup did not complete")
	}
	if calls.Load() != 1 || r.state("scene").view.Load().active != 0 {
		t.Fatal("wait turn did not release ownership")
	}
	clock.Advance(49)
	if rechecks.Load() != 0 {
		t.Fatal("early wait expiry")
	}
	clock.Advance(1)
	clock.Advance(5000)
	if calls.Load() != 1 || rechecks.Load() != 1 {
		t.Fatalf("model=%d rechecks=%d", calls.Load(), rechecks.Load())
	}
}
func TestFreshGenerationInvalidatesRequestedWait(t *testing.T) {
	var calls atomic.Int64
	cleaned := make(chan struct{}, 2)
	release := make(chan func(Completion, error), 1)
	r, _, clock := testRuntime(t, ModelFunc(func(_ context.Context, _ ModelRequest, done func(Completion, error)) {
		if calls.Add(1) == 1 {
			release <- done
			return
		}
		done(fixtureDecision("silent"), nil)
	}))
	r.services.Trace = func(e Event) {
		if e.Stage == "turn_cleanup" && e.Status == "completed" {
			cleaned <- struct{}{}
		}
	}
	awaitCleanup := func() {
		t.Helper()
		select {
		case <-cleaned:
		case <-time.After(2 * time.Second):
			t.Fatal("generation cleanup did not complete")
		}
		if r.state("scene").view.Load().active != 0 {
			t.Fatal("generation turn did not release ownership")
		}
	}
	cfg := r.config()
	cfg.DebounceMS = 100
	cfg.MaxWaitMS = 400
	_ = r.Configure(cfg)
	registerFixture(t, r)
	_, _ = r.Ingest(context.Background(), "scene", fixtureMessage("scene", "scheduled", "Old input"), true)
	clock.Advance(100)
	settle := <-release
	_, _ = r.Ingest(context.Background(), "scene", fixtureMessage("scene", "fresh", "New input"), true)
	out := fixtureDecision("wait")
	_ = out.ToolCalls[0].Arguments.(*Object).Set("waitForMs", float64(60000))
	settle(out, nil)
	// active=0 alone precedes generation finalization and timer installation.
	awaitCleanup()
	clock.Advance(99)
	if calls.Load() != 1 {
		t.Fatal("fresh generation skipped quiet interval")
	}
	clock.Advance(1)
	awaitCleanup()
	if calls.Load() != 2 {
		t.Fatal("fresh generation did not run exactly once")
	}
	clock.Advance(60000)
	if calls.Load() != 2 {
		t.Fatal("stale WAIT minted model work")
	}
}
func TestCancelUnknownTransmissionNeverRetriesOrLosesProtection(t *testing.T) {
	entered := make(chan *dispatch.Future[dispatch.Receipt], 1)
	var sends atomic.Int64
	r, _, clock := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		switch req.Stage {
		case "decision":
			done(fixtureDecision("reply"), nil)
		case "plan":
			done(fixturePlan(req, "reply", nil), nil)
		default:
			done(completion([]replyplan.ToolCall{fixtureSend("First", true)}, ""), nil)
		}
	}))
	cfg := r.config()
	cfg.TopicAdmissionEnabled = true
	_ = r.Configure(cfg)
	topic := registerFixture(t, r)
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		if e := before(); e != nil {
			return dispatch.Rejected[dispatch.Receipt](e)
		}
		sends.Add(1)
		future := dispatch.NewFuture[dispatch.Receipt]()
		entered <- future
		return future
	}}})
	h, _ := r.Run("scene", topic, false)
	future := <-entered
	r.Cancel("scene")
	result := awaitTurn(t, h)
	if result.Counts["unknownSegments"] != 1 || result.Status != "partial" {
		t.Fatalf("lost uncertain transmission: %+v", result)
	}
	future.Resolve(dispatch.Receipt{Status: dispatch.Sent, MessageID: ptr("too-late")})
	clock.Advance(600000)
	snapshot, _ := r.Snapshot("scene")
	if sends.Load() != 1 || len(snapshot.Protected) != 1 || snapshot.Protected[0] != topic {
		t.Fatalf("unknown send retried/unprotected: sends=%d snapshot=%+v", sends.Load(), snapshot)
	}
}
func TestTransmitGateRechecksAfterAsyncAdapterPreparation(t *testing.T) {
	entered := make(chan func() error, 1)
	settlement := make(chan *dispatch.Future[dispatch.Receipt], 1)
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		switch req.Stage {
		case "decision":
			done(fixtureDecision("reply"), nil)
		case "plan":
			done(fixturePlan(req, "reply", nil), nil)
		default:
			done(completion([]replyplan.ToolCall{fixtureSend("old", false)}, ""), nil)
		}
	}))
	topic := registerFixture(t, r)
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{Send: func(_ dispatch.SceneRef, _ dispatch.Segment, _ *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		future := dispatch.NewFuture[dispatch.Receipt]()
		entered <- before
		settlement <- future
		return future
	}}})
	h, _ := r.Run("scene", topic, false)
	before := <-entered
	future := <-settlement
	_, _ = r.Ingest(context.Background(), "scene", fixtureMessage("scene", "correction", "Correction"), false)
	if err := before(); err == nil {
		t.Fatal("stale version authorized physical effect")
	}
	future.Resolve(dispatch.Receipt{Status: dispatch.Cancelled, Detail: ptr("send_invalidated")})
	result := awaitTurn(t, h)
	if result.Counts["sentSegments"] != 0 {
		t.Fatal(result)
	}
}
func TestReadCannotCrossScenesAndSimulationNeverReadsRealAdapter(t *testing.T) {
	var reads atomic.Int64
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		done(fixtureDecision("silent"), nil)
	}))
	topic := registerFixture(t, r)
	r.AddAdapter("fixture", &Adapter{Transport: &dispatch.Adapter{}, GetMessage: func(_ context.Context, _ Scene, _ string, done func(*Object, error)) {
		reads.Add(1)
		done(obj("items", []any{fixtureMessage("other", "secret", "foreign")}, "coverage", "complete"), nil)
	}})
	s := r.state("scene")
	s.mu.Lock()
	turn, e := r.startLocked(s, topic, false, nil)
	r.publishLocked(s)
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	output, e := r.read(turn, context.Background(), "messages.get", obj("ids", []any{"secret"}))
	if e != nil || len(array(get(output, "items"))) != 0 {
		t.Fatal("cross-scene history leaked", output, e)
	}
	r.finish(turn, 0)
	sim := fixtureScene(true)
	if e = r.RegisterScene(sim); e != nil {
		t.Fatal(e)
	}
	id, e := r.Ingest(context.Background(), sim.ID, fixtureMessage(sim.ID, "source", "Simulation"), false)
	if e != nil {
		t.Fatal(e)
	}
	ss := r.state(sim.ID)
	ss.mu.Lock()
	st, e := r.startLocked(ss, id, false, nil)
	r.publishLocked(ss)
	ss.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	output, e = r.read(st, context.Background(), "messages.get", obj("ids", []any{"secret"}))
	r.finish(st, 0)
	if e != nil || len(array(get(output, "items"))) != 0 || reads.Load() != 1 {
		t.Fatal("simulation invoked real history", reads.Load(), e)
	}
}
func TestPersistentBodyViewSharesHistoryAndQueryCopiesAtRead(t *testing.T) {
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, _ ModelRequest, done func(Completion, error)) {
		done(fixtureDecision("silent"), nil)
	}))
	registerFixture(t, r)
	s := r.state("scene")
	first := s.view.Load().history
	firstMessage := first.rows[0]
	for i := 0; i < 33; i++ {
		_, e := r.Ingest(context.Background(), "scene", fixtureMessage("scene", string(rune(100+i)), "same immutable body"), false)
		if e != nil {
			t.Fatal(e)
		}
	}
	latest := s.view.Load().history
	if latest.previous == nil || latest.previous.rows[0] != firstMessage {
		t.Fatal("append cloned prior bodies")
	}
	if first.count != 1 || len(first.rows) != 1 {
		t.Fatal("published old block changed")
	}
	snapshot, _ := r.Snapshot("scene")
	_ = snapshot.Messages[0].Set("parts", []any{})
	if len(array(get(s.view.Load().history.previous.rows[0], "parts"))) == 0 {
		t.Fatal("query mutated runtime body")
	}
}
func TestOperationLookupDoesNotInspectUnrelatedTurns(t *testing.T) {
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, _ ModelRequest, done func(Completion, error)) {
		done(fixtureDecision("silent"), nil)
	}))
	registerFixture(t, r)
	owner := &turn{state: r.state("scene")}
	op := &receiptOperation{key: "owned", owner: owner, receipts: []dispatch.Receipt{{Status: dispatch.Unknown}}}
	r.operationMu.Lock()
	r.operations.Set("owned", op)
	owner.operations.Set("owned", op)
	owner.outcomeCount = 1
	owner.state.operationKeys["owned"] = true
	for i := 0; i < 1000; i++ {
		r.operations.Set(string(rune(1000+i)), nil)
	}
	r.operationMu.Unlock()
	if !r.hasOutcome(owner) || len(r.outcomes(owner)) != 1 {
		t.Fatal("missing own receipt")
	}
	r.operationMu.Lock()
	for _, e := range r.operations.Entries() {
		if e.Value == nil {
			r.operations.Delete(e.Key)
		}
	}
	r.deleteOperationLocked("owned")
	r.operationMu.Unlock()
	if r.hasOutcome(owner) || len(r.outcomes(owner)) != 0 || len(owner.state.operationKeys) != 0 {
		t.Fatal("deletion did not update ownership indexes")
	}
}
func TestSimulateConcurrentAdmissionAddsOnlyOneInput(t *testing.T) {
	entered := make(chan func(Completion, error), 1)
	r, a, _ := testRuntime(t, ModelFunc(func(_ context.Context, _ ModelRequest, done func(Completion, error)) { entered <- done }))
	id, e := r.CreateSimulation()
	if e != nil {
		t.Fatal(e)
	}
	first := make(chan error, 1)
	go func() { _, err := r.Simulate(context.Background(), id, "first"); first <- err }()
	settle := <-entered
	_, err := r.Simulate(context.Background(), id, "second")
	if err == nil || err.Error() != "simulation_busy" {
		t.Fatal("concurrent simulation admitted", err)
	}
	snapshot, _ := r.Snapshot(id)
	if len(snapshot.Messages) != 1 || GroundingText(rawParts(snapshot.Messages[0])) != "first" {
		t.Fatal("rejected simulation appended input")
	}
	settle(fixtureDecision("silent"), nil)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if a.writes != 0 {
		t.Fatal("simulation persisted")
	}
}
func TestMessageMetadataParsedOnceAndUnexpiredTrimIsConstant(t *testing.T) {
	r, _, clock := testRuntime(t, ModelFunc(func(_ context.Context, _ ModelRequest, done func(Completion, error)) {
		done(fixtureDecision("silent"), nil)
	}))
	registerFixture(t, r)
	s := r.state("scene")
	for i := 0; i < 200; i++ {
		_, e := r.Ingest(context.Background(), "scene", fixtureMessage("scene", string(rune(1000+i)), "fresh"), false)
		if e != nil {
			t.Fatal(e)
		}
	}
	s.mu.Lock()
	parsed, visits := s.parsedBodies, s.trimVisits
	size := len(s.byID)
	s.mu.Unlock()
	if parsed != 201 || visits != 0 || size != 201 {
		t.Fatalf("unexpired ingress repeated parse/trim: parse=%d visits=%d indexed=%d", parsed, visits, size)
	}
	for i := 0; i < 10; i++ {
		if _, e := r.Snapshot("scene"); e != nil {
			t.Fatal(e)
		}
	}
	clock.Advance(25 * 3600000)
	r.CleanupIdle()
	if state := r.state("scene"); state != nil {
		state.mu.Lock()
		if state.parsedBodies != parsed || len(state.byID) != 0 {
			t.Error("expiry reparsed bodies or retained stale index")
		}
		state.mu.Unlock()
	}
}
func TestSimulationRecoveryAndRawConfigurationAreNotBypassed(t *testing.T) {
	for _, window := range []bool{true, false} {
		t.Run(map[bool]string{true: "window", false: "no-window"}[window], func(t *testing.T) {
			var calls atomic.Int64
			r, a, _ := testRuntime(t, ModelFunc(func(_ context.Context, _ ModelRequest, done func(Completion, error)) {
				calls.Add(1)
				done(fixtureDecision("silent"), nil)
			}))
			cfg := r.config()
			cfg.PromptContextWindowEnabled = window
			_ = r.Configure(cfg)
			scene := fixtureScene(true)
			_ = r.RegisterScene(scene)
			topic, e := r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "source", "simulation"), false)
			if e != nil {
				t.Fatal(e)
			}
			a.mu.Lock()
			a.readyErr = code("recovery_gate_invalid")
			a.mu.Unlock()
			h, e := r.Run(scene.ID, topic, false)
			if e != nil {
				t.Fatal(e)
			}
			result := awaitTurn(t, h)
			if calls.Load() != 0 || result.Status != "cancelled" {
				t.Fatal("simulation bypassed recovery", calls.Load(), result)
			}
			a.mu.Lock()
			a.readyErr = nil
			a.recalled[scene.ID+"|source"] = true
			a.mu.Unlock()
			h, _ = r.Run(scene.ID, topic, false)
			result = awaitTurn(t, h)
			if len(result.Receipts) != 0 {
				t.Fatal("simulation bypassed scoped dependency tombstone")
			}
		})
	}
}
func TestRecallFailureStopsCallbacksAtExactSourceBoundary(t *testing.T) {
	data, e := os.ReadFile("testdata/recall.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name     string
		Expected struct {
			Calls               []string
			Error, SourceStatus string
			Aborted             bool
		}
	}
	if e = json.Unmarshal(data, &cases); e != nil {
		t.Fatal(e)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			entered := make(chan *TurnContext, 1)
			var recording atomic.Bool
			var callsMu sync.Mutex
			calls := []string{}
			record := func(s string) { callsMu.Lock(); calls = append(calls, s); callsMu.Unlock() }
			r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, _ func(Completion, error)) { entered <- req.Turn }))
			r.AddExtension(&Extension{ContextData: func(c *TurnContext) (any, error) {
				c.AddValidation(func() bool {
					if recording.Load() {
						record("validate")
					}
					return true
				})
				return nil, nil
			}, PrepareRecall: func(_ string, _ string) (func() error, error) {
				record("prepare")
				if test.Name == "prepareFailed" {
					return nil, code("prepare_failed")
				}
				return func() error { record("callback"); return code("callback_failed") }, nil
			}})
			topic := registerFixture(t, r)
			h, _ := r.Run("scene", topic, false)
			turn := <-entered
			recording.Store(true)
			err := r.Recall(context.Background(), "scene", "source")
			recording.Store(false)
			snapshot := r.state("scene").view.Load()
			rows := bodyRows(snapshot.history)
			callsMu.Lock()
			got := append([]string{}, calls...)
			callsMu.Unlock()
			if err == nil || err.Error() != test.Expected.Error || !reflect.DeepEqual(got, test.Expected.Calls) || get(rows[0], "status") != test.Expected.SourceStatus || turn.root.Aborted() != test.Expected.Aborted {
				t.Fatalf("got calls=%v err=%v status=%v aborted=%v source=%+v", got, err, get(rows[0], "status"), turn.root.Aborted(), test.Expected)
			}
			r.Cancel("scene")
			awaitTurn(t, h)
		})
	}
}

func TestExtensionRemovalInvalidatesPendingPromptAuthority(t *testing.T) {
	entered := make(chan ModelRequest, 1)
	release := make(chan func(Completion, error), 1)
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		entered <- req
		release <- done
	}))
	remove := r.AddExtension(&Extension{Tools: []*Object{obj("name", "fixture.dynamic", "inputSchema", obj("type", "object"))}})
	topic := registerFixture(t, r)
	h, err := r.Run("scene", topic, false)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	remove()
	remove()
	(<-release)(fixtureDecision("reply"), nil)
	result := awaitTurn(t, h)
	if result.Reason != "decision_reply" || len(result.Receipts) != 0 {
		t.Fatalf("removed extension kept prompt authority: %+v", result)
	}
}

func TestOldSimulationCleanupCannotDeleteReplacementOwner(t *testing.T) {
	entered := make(chan ModelRequest, 2)
	release := make(chan func(Completion, error), 2)
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, req ModelRequest, done func(Completion, error)) {
		entered <- req
		release <- done
	}))
	scene := fixtureScene(true)
	if err := r.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	topic, err := r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "source", "Old turn"), false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.Run(scene.ID, topic, false)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	oldRelease := <-release
	r.EndSimulation(scene.ID)
	if err := r.RegisterScene(scene); err != nil {
		t.Fatal(err)
	}
	replacement := r.state(scene.ID)
	topic, err = r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "source", "Replacement turn"), false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Run(scene.ID, topic, false)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	newRelease := <-release
	oldRelease(fixtureDecision("reply"), nil)
	awaitTurn(t, first)
	if r.state(scene.ID) != replacement || replacement.view.Load().active != 1 {
		t.Fatal("old cleanup changed replacement owner")
	}
	newRelease(fixtureDecision("silent"), nil)
	awaitTurn(t, second)
	view, err := r.Snapshot(scene.ID)
	if err != nil || len(view.Messages) != 1 || view.Active != 0 {
		t.Fatalf("replacement state: %+v %v", view, err)
	}
}
