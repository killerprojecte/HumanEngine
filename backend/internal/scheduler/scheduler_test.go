package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

type number float64

func (n *number) UnmarshalJSON(raw []byte) error {
	var value float64
	if err := json.Unmarshal(raw, &value); err == nil {
		*n = number(value)
		return nil
	}
	var tag struct {
		Number string `json:"number"`
	}
	if err := json.Unmarshal(raw, &tag); err != nil {
		return err
	}
	switch tag.Number {
	case "NaN":
		*n = number(math.NaN())
	case "Infinity":
		*n = number(math.Inf(1))
	case "-Infinity":
		*n = number(math.Inf(-1))
	default:
		return fmt.Errorf("unknown number %q", tag.Number)
	}
	return nil
}
func ptr(n *number) *float64 {
	if n == nil {
		return nil
	}
	v := float64(*n)
	return &v
}

type fixtureConfig struct {
	Concurrency       number
	MaxQueued         number
	ReserveForeground number
	BackgroundAgingMS *number
}
type fixtureStep struct {
	Op, ID, Failure, Trace                    string
	Priority, Concurrency, CallsPerMinute, At number
	Auto, Aborted, Model                      bool
	OverdueLearningAt                         *number
	ScopePriorities, ScopeDeadlines           []number
}
type timer struct {
	clock    *fakeClock
	at       float64
	id       int
	callback func()
	active   bool
}

func (t *timer) Stop() bool { was := t.active; t.active = false; return was }

type fakeClock struct {
	now    float64
	timers []*timer
}

func (c *fakeClock) Now() float64 { return c.now }
func (c *fakeClock) AfterFunc(ms float64, callback func()) Timer {
	t := &timer{clock: c, at: c.now + math.Trunc(ms), id: len(c.timers), callback: callback, active: true}
	c.timers = append(c.timers, t)
	return t
}
func (c *fakeClock) pending() []float64 {
	v := []float64{}
	for _, t := range c.timers {
		if t.active {
			v = append(v, t.at)
		}
	}
	sort.Float64s(v)
	return v
}
func (c *fakeClock) next() *timer {
	var first *timer
	for _, t := range c.timers {
		if t.active && t.at <= c.now && (first == nil || t.at < first.at || (t.at == first.at && t.id < first.id)) {
			first = t
		}
	}
	return first
}
func opt(value any, present bool) map[string]any {
	if !present {
		return map[string]any{"present": false}
	}
	return map[string]any{"present": true, "value": value}
}

type traceKey struct{}
type traceJob struct {
	future   *Future
	complete func(any, error)
	early    bool
	failure  string
	previous string
}

func failError(failure string) error {
	if failure == "nonerror" {
		return ErrWorkFailed
	}
	if failure != "" {
		return errors.New("fixture_failure")
	}
	return nil
}
func compare(t *testing.T, got any, want json.RawMessage) {
	t.Helper()
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err = json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got %s\nwant %s", data, want)
	}
}
func TestNodeDifferentialTraces(t *testing.T) {
	data, err := os.ReadFile("testdata/traces-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version                              int
		Baseline, Source, SourceSHA256, Node string
		InvalidOptions                       []struct {
			Options fixtureConfig
			Error   string
		}
		Scopes []struct {
			Priorities, Deadlines []number
			Expected              json.RawMessage
		}
		Traces []struct {
			Name   string
			Config fixtureConfig
			Steps  []struct {
				Input    fixtureStep
				Expected json.RawMessage
			}
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.Baseline != "24fe8a773d87f2f09996e259fca46524c2c02928" || fixture.Source != "src/scheduler.ts" || fixture.Node != "v24.22.1" {
		t.Fatal("unexpected oracle identity")
	}
	source, err := os.ReadFile("../../../" + fixture.Source)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(source)
	if hex.EncodeToString(hash[:]) != fixture.SourceSHA256 || fixture.SourceSHA256 != "3340a98e5545c146a2332dc335931efeb8c577806d08cb2d05f1c92d30b52d36" {
		t.Fatal("scheduler source changed without baseline review")
	}
	if len(fixture.Traces) != 41 || len(fixture.InvalidOptions) != 12 || len(fixture.Scopes) != 64 {
		t.Fatal("fixture coverage changed")
	}
	for _, test := range fixture.InvalidOptions {
		_, err := New(1, 256, Options{ReserveForeground: float64(test.Options.ReserveForeground), BackgroundAgingMS: ptr(test.Options.BackgroundAgingMS)})
		if err == nil || err.Error() != test.Error {
			t.Fatalf("invalid options: %v", err)
		}
	}
	for i, test := range fixture.Scopes {
		t.Run(fmt.Sprintf("scope_%d", i), func(t *testing.T) {
			ctx := context.Background()
			var err error
			for _, p := range test.Priorities {
				ctx, err = WithModelPriority(ctx, float64(p))
				if err != nil {
					break
				}
			}
			if err == nil {
				for _, d := range test.Deadlines {
					ctx, err = WithOverdueLearning(ctx, float64(d))
					if err != nil {
						break
					}
				}
			}
			if err != nil {
				compare(t, map[string]any{"error": err.Error()}, test.Expected)
			} else {
				d, ok := GetOverdueLearningAt(ctx)
				compare(t, map[string]any{"priority": EffectiveModelPriority(ctx, 0), "higherBase": EffectiveModelPriority(ctx, 3), "deadline": opt(d, ok)}, test.Expected)
			}
		})
	}
	for _, trace := range fixture.Traces {
		t.Run(trace.Name, func(t *testing.T) {
			c := &fakeClock{}
			s, err := New(float64(trace.Config.Concurrency), float64(trace.Config.MaxQueued), Options{ReserveForeground: float64(trace.Config.ReserveForeground), BackgroundAgingMS: ptr(trace.Config.BackgroundAgingMS), Clock: c})
			if err != nil {
				t.Fatal(err)
			}
			var dispatch []func()
			s.launch = func(f func()) { dispatch = append(dispatch, f) }
			flush := func() {
				for len(dispatch) > 0 {
					f := dispatch[0]
					dispatch = dispatch[1:]
					f()
				}
			}
			jobs := map[string]*traceJob{}
			events := []any{}
			for i, entry := range trace.Steps {
				step := entry.Input
				var operationError error
				switch step.Op {
				case "run":
					ctx := context.Background()
					for _, p := range step.ScopePriorities {
						ctx, err = WithModelPriority(ctx, float64(p))
						if err != nil {
							t.Fatal(err)
						}
					}
					for _, d := range step.ScopeDeadlines {
						ctx, err = WithOverdueLearning(ctx, float64(d))
						if err != nil {
							t.Fatal(err)
						}
					}
					if step.Trace != "" {
						ctx = context.WithValue(ctx, traceKey{}, step.Trace)
					}
					if step.Aborted {
						cancelled, cancel := context.WithCancel(ctx)
						cancel()
						ctx = cancelled
					}
					j := &traceJob{}
					jobs[step.ID] = j
					task := func(ctx context.Context, complete func(any, error)) {
						d, ok := GetOverdueLearningAt(ctx)
						trace, hasTrace := ctx.Value(traceKey{}).(string)
						events = append(events, map[string]any{"id": step.ID, "at": c.now, "priority": EffectiveModelPriority(ctx, 0), "deadline": opt(d, ok), "trace": opt(trace, hasTrace), "aborted": ctx.Err() != nil})
						j.complete = complete
						if step.Auto {
							complete(step.ID, nil)
						} else if j.early {
							complete(step.ID, failError(j.failure))
						}
					}
					if step.Model {
						j.future = s.SubmitModel(ctx, float64(step.Priority), task)
					} else {
						j.future = s.Submit(ctx, float64(step.Priority), task, SchedulingOptions{OverdueLearningAt: ptr(step.OverdueLearningAt)})
					}
				case "complete":
					j := jobs[step.ID]
					if !j.early {
						j.early = true
						j.failure = step.Failure
						if j.complete != nil {
							j.complete(step.ID, failError(step.Failure))
						}
					}
				case "abort":
					jobs[step.ID].future.Cancel()
				case "configure":
					operationError = s.Configure(float64(step.Concurrency), float64(step.CallsPerMinute))
				case "advance":
					c.now = float64(step.At)
					for {
						timer := c.next()
						if timer == nil {
							break
						}
						timer.active = false
						timer.callback()
						flush()
					}
				default:
					t.Fatalf("unknown step %s", step.Op)
				}
				flush()
				changes := map[string]any{}
				for id, j := range jobs {
					state := "pending"
					result := map[string]any{}
					select {
					case <-j.future.Done():
						value, err := j.future.Wait()
						if err != nil {
							state = "rejected"
							result["error"] = err.Error()
						} else {
							state = "resolved"
							result["value"] = value
						}
					default:
					}
					if state != j.previous {
						j.previous = state
						result["state"] = state
						changes[id] = result
					}
				}
				got := map[string]any{"status": s.Status(), "events": events, "jobs": changes, "timers": c.pending()}
				if operationError != nil {
					got["error"] = operationError.Error()
				}
				t.Run(fmt.Sprintf("step_%d_%s", i, step.Op), func(t *testing.T) { compare(t, got, entry.Expected) })
				if t.Failed() {
					return
				}
				events = []any{}
			}
		})
	}
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("task did not finish")
	}
}
func TestConcurrentCancellationOwnsRunningSlot(t *testing.T) {
	s, _ := New(2, 256, Options{ReserveForeground: 1})
	started := make(chan context.Context, 1)
	finish := make(chan func(any, error), 1)
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), traceKey{}, "trace"))
	active := s.Submit(parent, 2, func(ctx context.Context, done func(any, error)) { started <- ctx; finish <- done }, SchedulingOptions{})
	ctx := <-started
	cancel()
	await(t, ctx.Done())
	if ctx.Value(traceKey{}) != "trace" {
		t.Fatal("lost caller context")
	}
	nextStarted := make(chan struct{})
	next := s.Submit(context.Background(), 2, func(_ context.Context, done func(any, error)) { close(nextStarted); done("next", nil) }, SchedulingOptions{})
	if status := s.Status(); status.Running != 1 || status.Queued != 1 {
		t.Fatalf("running cancellation freed slot: %+v", status)
	}
	select {
	case <-nextStarted:
		t.Fatal("next task started prematurely")
	default:
	}
	complete := <-finish
	complete("original", nil)
	complete("duplicate", errors.New("duplicate"))
	await(t, active.Done())
	value, err := active.Wait()
	if value != "original" || err != nil {
		t.Fatalf("result %v %v", value, err)
	}
	await(t, next.Done())
}
func TestQueuedParentCancellationAndNoCrossRequestLeak(t *testing.T) {
	s, _ := New(1, 256, Options{})
	finish := make(chan func(any, error), 1)
	active := s.Submit(context.Background(), 0, func(_ context.Context, done func(any, error)) { finish <- done }, SchedulingOptions{})
	complete := <-finish
	ctx, cancel := context.WithCancel(context.Background())
	queued := s.Submit(ctx, 0, func(context.Context, func(any, error)) { t.Error("cancelled callback ran") }, SchedulingOptions{})
	cancel()
	await(t, queued.Done())
	if _, err := queued.Wait(); err != ErrCancelled {
		t.Fatal(err)
	}
	if status := s.Status(); status.Running != 1 || status.Queued != 0 {
		t.Fatalf("incorrect ownership %+v", status)
	}
	complete(nil, nil)
	await(t, active.Done())
}
func TestParallelSubmitCompleteAndCancel(t *testing.T) {
	s, _ := New(8, 256, Options{ReserveForeground: 1})
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := s.Submit(context.Background(), float64(i%3), func(ctx context.Context, done func(any, error)) { go func() { done(i, nil); done(-1, nil) }() }, SchedulingOptions{})
			if i%4 == 0 {
				f.Cancel()
			}
			await(t, f.Done())
			_, err := f.Wait()
			if err != nil && err != ErrCancelled {
				t.Errorf("unexpected result %v", err)
			}
		}(i)
	}
	wg.Wait()
	// Wait for the final dispatch to release its slot, independently of delivery.
	deadline := time.Now().Add(5 * time.Second)
	for s.Status().Running != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if status := s.Status(); status.Running != 0 || status.Queued != 0 {
		t.Fatalf("leaked work %+v", status)
	}
}
func TestReentrantSubmissionAndPanics(t *testing.T) {
	s, _ := New(1, 256, Options{})
	nested := make(chan *Future, 1)
	outer := s.Submit(context.Background(), 0, func(ctx context.Context, done func(any, error)) {
		nested <- s.Submit(ctx, 0, func(context.Context, func(any, error)) { panic("not an error") }, SchedulingOptions{})
		done("outer", nil)
	}, SchedulingOptions{})
	await(t, outer.Done())
	inner := <-nested
	await(t, inner.Done())
	if _, err := inner.Wait(); err != ErrWorkFailed {
		t.Fatalf("panic normalization: %v", err)
	}
	original := errors.New("identity")
	f := s.Submit(context.Background(), 0, func(context.Context, func(any, error)) { panic(original) }, SchedulingOptions{})
	await(t, f.Done())
	if _, err := f.Wait(); err != original {
		t.Fatalf("lost error identity: %v", err)
	}
}

func TestCancellationBetweenAdmissionAndInvocation(t *testing.T) {
	c := &fakeClock{}
	s, _ := New(1, 256, Options{Clock: c})
	var dispatch func()
	s.launch = func(f func()) { dispatch = f }
	s.Configure(1, 1)
	called := false
	f := s.Submit(context.Background(), 0, func(ctx context.Context, done func(any, error)) {
		called = true
		if ctx.Err() != context.Canceled {
			t.Error("admitted callback lost cancellation")
		}
		done("accepted", nil)
	}, SchedulingOptions{})
	f.Cancel()
	if status := s.Status(); status.Running != 1 || status.Queued != 0 {
		t.Fatalf("admitted slot released: %+v", status)
	}
	dispatch()
	value, err := f.Wait()
	if !called || value != "accepted" || err != nil {
		t.Fatalf("admitted callback skipped: %v %v %v", called, value, err)
	}
	// A cancelled admitted request still spends its start; queued cancellations do not.
	waiting := s.Submit(context.Background(), 0, func(context.Context, func(any, error)) { t.Error("rate-wait task invoked") }, SchedulingOptions{})
	if !reflect.DeepEqual(c.pending(), []float64{60000}) {
		t.Fatalf("lost start accounting: %v", c.pending())
	}
	waiting.Cancel()
	if len(c.pending()) != 0 {
		t.Fatal("cancelled wait left a timer")
	}
}
func TestStaleRateTimerCannotStealNewWake(t *testing.T) {
	c := &fakeClock{}
	s, _ := New(1, 256, Options{Clock: c})
	var dispatch []func()
	s.launch = func(f func()) { dispatch = append(dispatch, f) }
	flush := func() {
		for len(dispatch) > 0 {
			f := dispatch[0]
			dispatch = dispatch[1:]
			f()
		}
	}
	s.Configure(1, 1)
	s.Submit(context.Background(), 0, func(_ context.Context, done func(any, error)) { done(nil, nil) }, SchedulingOptions{})
	flush()
	f := s.Submit(context.Background(), 0, func(_ context.Context, done func(any, error)) { done(nil, nil) }, SchedulingOptions{})
	old := c.timers[len(c.timers)-1]
	s.Configure(1, 1)
	current := c.timers[len(c.timers)-1]
	old.callback()
	if !current.active || len(c.pending()) != 1 {
		t.Fatalf("stale callback changed timer ownership: %v", c.pending())
	}
	c.now = 60000
	current.active = false
	current.callback()
	flush()
	await(t, f.Done())
}
func TestFirstCompletionWinsEvenIfStarterPanicsLater(t *testing.T) {
	s := NewDefault()
	f := s.Submit(context.Background(), 0, func(_ context.Context, done func(any, error)) { done("first", nil); panic(errors.New("later")) }, SchedulingOptions{})
	await(t, f.Done())
	value, err := f.Wait()
	if value != "first" || err != nil {
		t.Fatalf("later panic changed settled completion: %v %v", value, err)
	}
}
func TestOptionsAreCapturedAndDefaultPolicies(t *testing.T) {
	age := float64(100)
	s, _ := New(4, 256, Options{ReserveForeground: 1, BackgroundAgingMS: &age})
	age = 0
	if *s.backgroundAgingMS != 100 {
		t.Fatal("caller mutation changed aging")
	}
	model := NewModelScheduler()
	if model.concurrency != 4 || model.maxQueued != 256 || model.reserveForeground != 1 || *model.backgroundAgingMS != 300000 {
		t.Fatal("model defaults changed")
	}
	plain := NewDefault()
	if plain.reserveForeground != 0 || plain.backgroundAgingMS != nil {
		t.Fatal("default scheduler acquired model policies")
	}
}
