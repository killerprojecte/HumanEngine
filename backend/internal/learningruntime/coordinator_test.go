package learningruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningbatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
	"github.com/killerprojecte/HumanEngine/backend/internal/topiccollation"
)

type fakeTimer struct {
	id  uint64
	due int64
	f   func()
}
type fakeClock struct {
	mu       sync.Mutex
	now      int64
	sequence uint64
	timers   map[uint64]fakeTimer
}

func (c *fakeClock) Now() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) AfterFunc(ms int64, f func()) func() {
	c.mu.Lock()
	c.sequence++
	id := c.sequence
	c.timers[id] = fakeTimer{id, c.now + ms, f}
	c.mu.Unlock()
	return func() { c.mu.Lock(); delete(c.timers, id); c.mu.Unlock() }
}
func (c *fakeClock) tick(at int64) {
	c.mu.Lock()
	c.now = at
	list := []fakeTimer{}
	for _, t := range c.timers {
		if t.due <= at {
			list = append(list, t)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].due != list[j].due {
			return list[i].due < list[j].due
		}
		return list[i].id < list[j].id
	})
	c.mu.Unlock()
	for _, t := range list {
		c.mu.Lock()
		_, ok := c.timers[t.id]
		delete(c.timers, t.id)
		c.mu.Unlock()
		if ok {
			t.f()
		}
	}
}

type testOwner struct {
	mu             sync.Mutex
	busy, disabled bool
	recalled       map[string]bool
	events         []Event
	onEvent        func(Event)
}

func (o *testOwner) CurrentSource(context.Context, *botruntime.Object) (bool, error) {
	return true, nil
}
func (o *testOwner) Enabled(context.Context, string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return !o.disabled, nil
}
func (o *testOwner) Recalled(_ context.Context, s, id string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.recalled[s+"|"+id], nil
}
func (o *testOwner) ForegroundBusy() bool { o.mu.Lock(); defer o.mu.Unlock(); return o.busy }
func (o *testOwner) Event(e Event) {
	o.mu.Lock()
	o.events = append(o.events, e)
	f := o.onEvent
	o.mu.Unlock()
	if f != nil {
		f(e)
	}
}

type testRunner struct {
	mu              sync.Mutex
	phases          []string
	started, writes []any
	gates           []chan struct{}
	queue           *Coordinator
}

func (r *testRunner) Run(ctx context.Context, scene string, messages []*botruntime.Object, p *learningbatch.Progress) (bool, error) {
	ids, texts := []string{}, []string{}
	for _, m := range messages {
		ids = append(ids, str(m, "id"))
		parts, _ := m.Get("parts")
		text := ""
		for _, x := range parts.([]any) {
			part := x.(*jsjson.Object)
			if str(part, "type") == "text" {
				if text != "" {
					text += " "
				}
				text += str(part, "text")
			}
		}
		texts = append(texts, text)
	}
	r.mu.Lock()
	phase := "empty"
	if len(r.started) < len(r.phases) {
		phase = r.phases[len(r.started)]
	}
	r.started = append(r.started, map[string]any{"scene": scene, "ids": ids, "texts": texts, "priority": scheduler.EffectiveModelPriority(ctx, 0), "budget": scheduler.EffectiveModelPriority(ctx, 0)})
	var gate chan struct{}
	if phase == "hold" {
		gate = make(chan struct{})
		r.gates = append(r.gates, gate)
	}
	r.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if ctx.Err() != nil {
		return false, nil
	}
	if phase == "retryable" {
		return false, modelservice.NewError("synthetic_retry", nil, true)
	}
	if phase == "failure" {
		return false, errors.New("synthetic_failure")
	}
	if phase == "partial" || phase == "ignored-cancel" || phase == "saved-cancel" || phase == "rejected-cancel" {
		p.Totals.CandidateCount = 2
		for i := 0; i < 2; i++ {
			if ctx.Err() != nil {
				return false, nil
			}
			p.Applying = true
			p.WritePending = true
			r.mu.Lock()
			r.writes = append(r.writes, map[string]any{"scene": scene, "content": []string{"Synthetic durable value 0", "Synthetic durable value 1"}[i]})
			r.mu.Unlock()
			if phase == "partial" && i == 1 {
				return false, errors.New("synthetic_postcommit_failure")
			}
			if phase != "partial" {
				r.queue.Recall(scene, "a")
			}
			p.WritePending = false
			switch phase {
			case "saved-cancel":
				p.Totals.SavedCount++
			case "rejected-cancel":
				p.Totals.RejectedCount++
			default:
				p.Totals.IgnoredCount++
			}
		}
	}
	return true, nil
}
func (r *testRunner) release() {
	r.mu.Lock()
	gates := r.gates
	r.gates = nil
	r.mu.Unlock()
	for _, g := range gates {
		close(g)
	}
}
func fixtureMessage(scene, id, text string) *botruntime.Object {
	if text == "" {
		text = "synthetic"
	}
	o := new(jsjson.Object)
	for k, v := range map[string]any{"id": id, "sceneId": scene, "senderId": "person", "origin": "live", "status": "available", "sentAt": "2026-01-01T00:00:00.000Z", "receivedAt": "2026-01-01T00:00:00.000Z"} {
		_ = o.Set(k, v)
	}
	p := new(jsjson.Object)
	_ = p.Set("type", "text")
	_ = p.Set("text", text)
	_ = o.Set("parts", []any{p})
	return o
}
func newFixture(t *testing.T, settings Settings, phases []string, busy bool) (*Coordinator, *fakeClock, *testOwner, *testRunner) {
	t.Helper()
	collator, e := topiccollation.New("en-US")
	if errors.Is(e, topiccollation.ErrUnavailable) && os.Getenv("REQUIRE_ICU") == "" {
		t.Skip("native ICU profile required")
	}
	if e != nil {
		t.Fatal(e)
	}
	clock := &fakeClock{timers: map[uint64]fakeTimer{}}
	owner := &testOwner{busy: busy, recalled: map[string]bool{}}
	runner := &testRunner{phases: phases, started: []any{}, writes: []any{}}
	c, e := New(owner, runner, Options{Settings: settings, Clock: clock, Collator: collator})
	if e != nil {
		t.Fatal(e)
	}
	runner.queue = c
	t.Cleanup(func() {
		c.Dispose()
		runner.release()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if e := c.CloseAndWait(ctx); e != nil {
			t.Error(e)
		}
		collator.Close()
	})
	return c, clock, owner, runner
}
func normalized(v any) any { b, _ := json.Marshal(v); var o any; _ = json.Unmarshal(b, &o); return o }
func snapshot(c *Coordinator, r *testRunner) map[string]any {
	c.mu.Lock()
	s := c.statusLocked()
	for i := range s.Failures {
		s.Failures[i].ID = "failure"
		s.Failures[i].CreatedAt = "clock"
	}
	batches := func(m map[string][]*botruntime.Object) []any {
		keys := []string{}
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []any{}
		for _, k := range keys {
			ids := []string{}
			for _, v := range m[k] {
				ids = append(ids, str(v, "id"))
			}
			out = append(out, []any{k, ids})
		}
		return out
	}
	active := map[string][]*botruntime.Object{}
	for scene, a := range c.active {
		active[scene] = a.messages
	}
	retries, due := []any{}, []any{}
	keys := []string{}
	for k := range c.retries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := c.retries[k]
		ids := []string{}
		for _, m := range v.messages {
			ids = append(ids, str(m, "id"))
		}
		retries = append(retries, []any{k, v.attempt, v.due, ids})
	}
	keys = nil
	for k := range c.timers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		due = append(due, []any{k, c.timers[k].due})
	}
	out := map[string]any{"status": s, "queued": batches(c.queued), "active": batches(active), "survivors": batches(c.survivors), "retries": retries, "due": due}
	c.mu.Unlock()
	r.mu.Lock()
	out["started"] = append([]any{}, r.started...)
	out["writes"] = append([]any{}, r.writes...)
	r.mu.Unlock()
	return normalized(out).(map[string]any)
}
func awaitSnapshot(t *testing.T, c *Coordinator, r *testRunner, want map[string]any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got map[string]any
	for time.Now().Before(deadline) {
		got = snapshot(c, r)
		if reflect.DeepEqual(got, want) {
			return
		}
		runtime.Gosched()
	}
	g, _ := json.MarshalIndent(got, "", "  ")
	w, _ := json.MarshalIndent(want, "", "  ")
	t.Fatalf("snapshot mismatch\ngot %s\nwant %s", g, w)
}
func TestActualSourceCoordinatorTimerAndEffectTraces(t *testing.T) {
	data, e := os.ReadFile("testdata/source.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Sources map[string]string
		Cases   []struct {
			Plan struct {
				Name     string
				Phases   []string
				Capacity int64
				Busy     bool
				Steps    [][]any
			}
			Trace  []map[string]any
			Events []map[string]any
		}
	}
	if e = json.Unmarshal(data, &fixture); e != nil {
		t.Fatal(e)
	}
	for p, want := range fixture.Sources {
		b, e := os.ReadFile(filepath.Join("../../..", p))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != want {
			t.Fatalf("source changed: %s", p)
		}
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Plan.Name, func(t *testing.T) {
			settings := Settings{10, 20, 30, 100, 100, 3, 5}
			if tc.Plan.Capacity > 0 {
				settings.QueueMaxMessages = tc.Plan.Capacity
				settings.QueueMaxTotalMessages = tc.Plan.Capacity
			}
			c, clock, owner, runner := newFixture(t, settings, tc.Plan.Phases, tc.Plan.Busy)
			for i, step := range tc.Plan.Steps {
				var result any
				switch step[0] {
				case "enqueue":
					text := ""
					if len(step) > 3 {
						text = step[3].(string)
					}
					if _, e := c.Enqueue(context.Background(), fixtureMessage(step[1].(string), step[2].(string), text)); e != nil {
						t.Fatal(e)
					}
				case "tick":
					clock.tick(int64(step[1].(float64)))
				case "recall":
					c.Recall(step[1].(string), step[2].(string))
				case "cancel":
					c.CancelScene(step[1].(string))
				case "release":
					runner.release()
				case "retry":
					id := "missing"
					s := c.Status()
					if len(s.Failures) > 0 {
						id = s.Failures[0].ID
					}
					ok, e := c.Retry(context.Background(), id)
					if e != nil {
						t.Fatal(e)
					}
					result = map[string]any{"ok": ok}
				case "discard":
					id := "missing"
					s := c.Status()
					if len(s.Failures) > 0 {
						id = s.Failures[0].ID
					}
					result = map[string]any{"ok": c.Discard(id)}
				}
				expected := map[string]any{}
				for k, v := range tc.Trace[i] {
					if k != "step" && k != "result" {
						expected[k] = v
					}
				}
				if !reflect.DeepEqual(normalized(result), tc.Trace[i]["result"]) {
					t.Fatalf("step %d result: %v != %v", i, result, tc.Trace[i]["result"])
				}
				awaitSnapshot(t, c, runner, expected)
			}
			owner.mu.Lock()
			events := append([]Event{}, owner.events...)
			owner.mu.Unlock()
			gotEvents := []any{}
			for _, event := range events {
				if event.Stage != "learning.batch" || event.Status == "skipped" {
					continue
				}
				counts := map[string]any{"inputCount": event.InputCount, "attempt": event.Attempt, "candidateCount": event.Totals.CandidateCount, "savedCount": event.Totals.SavedCount, "rejectedCount": event.Totals.RejectedCount, "ignoredCount": event.Totals.IgnoredCount}
				if event.NextStep == "retry_scheduled" {
					counts["retryDelayMs"] = event.RetryDelayMS
				} else if event.Status != "completed" {
					counts["unconfirmedCandidateCount"] = event.UnconfirmedCandidateCount
				}
				gotEvents = append(gotEvents, map[string]any{"status": event.Status, "reason": event.Reason, "nextStep": event.NextStep, "counts": counts})
			}
			if !reflect.DeepEqual(normalized(gotEvents), normalized(tc.Events)) {
				a, _ := json.Marshal(gotEvents)
				b, _ := json.Marshal(tc.Events)
				t.Fatalf("terminal effect trace: %s != %s", a, b)
			}
		})
	}
}
func TestCloseJoinsCancellationIgnoringBatchAndNoRestartReplay(t *testing.T) {
	c, clock, _, r := newFixture(t, Settings{10, 20, 30, 100, 100, 3, 5}, []string{"hold"}, false)
	_, _ = c.Enqueue(context.Background(), fixtureMessage("s", "a", ""))
	clock.tick(10)
	deadline := time.Now().Add(3 * time.Second)
	for {
		r.mu.Lock()
		started := len(r.gates) > 0
		r.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner not entered")
		}
		runtime.Gosched()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := c.CloseAndWait(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("released physical work: %v", e)
	}
	if c.Status().Active != 1 {
		t.Fatal("cancelled active source stopped occupying resources")
	}
	if result, _ := c.Enqueue(context.Background(), fixtureMessage("s", "new", "")); result != "runtime_stopped" {
		t.Fatal(result)
	}
	r.release()
	if e := c.CloseAndWait(context.Background()); e != nil {
		t.Fatal(e)
	}
	if s := c.Status(); s.Active != 0 || s.Queued != 0 || s.Retrying != 0 {
		t.Fatal(s)
	}
}
func TestAdmissionLoggingCanRecallWithoutDeadlockOrRevival(t *testing.T) {
	c, _, owner, _ := newFixture(t, Settings{10, 20, 30, 100, 100, 3, 5}, nil, false)
	owner.onEvent = func(e Event) {
		if e.Stage == "learning.admission" && e.Status == "completed" {
			c.Recall("s", "a")
		}
	}
	_, e := c.Enqueue(context.Background(), fixtureMessage("s", "a", ""))
	if e != nil {
		t.Fatal(e)
	}
	if s := c.Status(); s.Queued != 0 {
		t.Fatal(s)
	}
}

func TestNativeExtensionSourceBoundaries(t *testing.T) {
	c, _, _, _ := newFixture(t, Settings{10, 20, 30, 100, 100, 3, 5}, nil, false)
	extension := c.Extension()
	scene := botruntime.Scene{}
	scene.ID = "s"
	scene.LearningEnabled = true
	limited := fixtureMessage("s", "limited", "")
	_ = limited.Set("sourceEventRegistrationLimited", true)
	if e := extension.Received(limited, scene); e != nil {
		t.Fatal(e)
	}
	if c.Status().Accepted != 0 {
		t.Fatal("limited source admitted")
	}
	original := fixtureMessage("s", "a", "original")
	if e := extension.Received(original, scene); e != nil {
		t.Fatal(e)
	}
	replacement := fixtureMessage("s", "a", "replacement")
	if e := extension.Edited(original, replacement, scene); e != nil {
		t.Fatal(e)
	}
	if e := extension.Received(replacement, scene); e != nil {
		t.Fatal(e)
	}
	c.mu.Lock()
	text := str(c.queued["s"][0], "id")
	c.mu.Unlock()
	if text != "a" || c.Status().Accepted != 2 || c.Status().Queued != 1 {
		t.Fatal(c.Status())
	}
	apply, e := extension.PrepareRecall("s", "a")
	if e != nil {
		t.Fatal(e)
	}
	if c.Status().Queued != 1 {
		t.Fatal("prepare mutated before recall owner applied")
	}
	if e = apply(); e != nil {
		t.Fatal(e)
	}
	if c.Status().Queued != 0 {
		t.Fatal("recalled source retained")
	}
	if e := extension.Received(fixtureMessage("s", "b", ""), scene); e != nil {
		t.Fatal(e)
	}
	scene.LearningEnabled = false
	extension.SceneChanged(scene)
	scene.LearningEnabled = true
	extension.SceneChanged(scene)
	if c.Status().Queued != 0 {
		t.Fatal("off-on revived input")
	}
}

type pausedAdmissionOwner struct {
	*testOwner
	entered, release chan struct{}
	before           bool
	once             sync.Once
}

func (o *pausedAdmissionOwner) Recalled(ctx context.Context, scene, id string) (bool, error) {
	if !o.before {
		o.once.Do(func() { close(o.entered); <-o.release })
	}
	return o.testOwner.Recalled(ctx, scene, id)
}
func (o *pausedAdmissionOwner) Enabled(ctx context.Context, scene string) (bool, error) {
	if o.before {
		o.once.Do(func() { close(o.entered); <-o.release })
	}
	return o.testOwner.Enabled(ctx, scene)
}
func TestAdmissionCannotCrossRecallOrSceneDisable(t *testing.T) {
	for _, mutation := range []string{"recall-edit", "recall-edit-before", "off-on", "off-on-before", "dispose", "dispose-before"} {
		t.Run(mutation, func(t *testing.T) {
			c, _, owner, _ := newFixture(t, DefaultSettings(), nil, false)
			paused := &pausedAdmissionOwner{testOwner: owner, entered: make(chan struct{}), release: make(chan struct{}), before: strings.HasSuffix(mutation, "-before")}
			c.owner = paused
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(paused.release) }); _ = c.CloseAndWait(context.Background()) })
			old := fixtureMessage("s", "m", "")
			returned := make(chan string, 1)
			go func() {
				status, e := c.Enqueue(context.Background(), old)
				if e != nil {
					status = e.Error()
				}
				returned <- status
			}()
			select {
			case <-paused.entered:
			case <-time.After(time.Second):
				t.Fatal("eligibility did not enter")
			}
			want := "source_unavailable"
			switch strings.TrimSuffix(mutation, "-before") {
			case "recall-edit":
				c.Recall("s", "m")
			case "off-on":
				c.CancelScene("s")
				want = "learning_disabled"
			case "dispose":
				c.Dispose()
				want = "runtime_stopped"
			}
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			if strings.HasPrefix(mutation, "dispose") {
				if e := c.CloseAndWait(cancelled); e == nil {
					t.Fatal("shutdown lost accepted source read")
				}
			}
			release.Do(func() { close(paused.release) })
			select {
			case got := <-returned:
				if got != want {
					t.Fatalf("old admission %s want %s", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("admission did not settle")
			}
			if got := c.Status(); got.Queued != 0 || got.Accepted != 0 {
				t.Fatalf("invalidated admission returned %+v", got)
			}
			if !strings.HasPrefix(mutation, "dispose") {
				if got, e := c.Enqueue(context.Background(), fixtureMessage("s", "m", "")); e != nil || got != "accepted" {
					t.Fatalf("replacement blocked %s %v", got, e)
				}
			}
		})
	}
}

type staleSourceOwner struct{ *testOwner }

func (o staleSourceOwner) CurrentSource(context.Context, *botruntime.Object) (bool, error) {
	return false, nil
}
func TestLateDeliveredOldSnapshotDoesNotReadmitAfterEdit(t *testing.T) {
	c, _, owner, _ := newFixture(t, DefaultSettings(), nil, false)
	c.owner = staleSourceOwner{owner}
	c.Recall("s", "m")
	if got, e := c.Enqueue(context.Background(), fixtureMessage("s", "m", "old")); e != nil || got != "source_unavailable" {
		t.Fatalf("stale source %s %v", got, e)
	}
	if c.Status().Queued != 0 {
		t.Fatal("old revision queued")
	}
	c.owner = owner
	if got, e := c.Enqueue(context.Background(), fixtureMessage("s", "m", "replacement")); e != nil || got != "accepted" {
		t.Fatal(got, e)
	}
}
