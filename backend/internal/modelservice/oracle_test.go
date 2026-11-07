package modelservice

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

type number float64

func (n *number) UnmarshalJSON(data []byte) error {
	var f float64
	if err := json.Unmarshal(data, &f); err == nil {
		*n = number(f)
		return nil
	}
	var tag struct{ Number string }
	if err := json.Unmarshal(data, &tag); err != nil {
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
		return fmt.Errorf("invalid number %s", data)
	}
	return nil
}
func numberPointer(n *number) *float64 {
	if n == nil {
		return nil
	}
	v := float64(*n)
	return &v
}
func n(v float64) *float64 { return &v }
func fixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/policy-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func decode(t *testing.T, raw json.RawMessage, output any) {
	t.Helper()
	if err := json.Unmarshal(raw, output); err != nil {
		t.Fatal(err)
	}
}
func same(t *testing.T, got any, want json.RawMessage) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	decode(t, raw, &a)
	decode(t, want, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("got %s\nwant %s", raw, want)
	}
}
func asJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func errorView(err error) map[string]any {
	result := map[string]any{"code": err.Error(), "retryable": false, "handled": ProviderAttemptsHandled(err), "failures": []string{}}
	if e, ok := err.(*ModelError); ok {
		result["code"] = e.Code
		result["retryable"] = e.Retryable
		if e.Status != nil {
			result["status"] = *e.Status
		}
		failures := []string{}
		for _, failure := range e.Failures() {
			if typed, ok := failure.(*ModelError); ok {
				failures = append(failures, typed.Code)
			} else {
				failures = append(failures, "plain")
			}
		}
		result["failures"] = failures
	}
	return result
}
func TestSourcePins(t *testing.T) {
	var sources map[string]string
	decode(t, fixture(t)["sources"], &sources)
	for path, want := range sources {
		body, err := os.ReadFile("../../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("source changed: %s: %s", path, got)
		}
	}
}
func TestPrimitiveOracle(t *testing.T) {
	f := fixture(t)
	var policies []struct {
		Input struct{ RetryCount, TimeoutMS *number }
		Value json.RawMessage
		Error string
	}
	decode(t, f["policies"], &policies)
	for i, test := range policies {
		t.Run(fmt.Sprintf("policy_%d", i), func(t *testing.T) {
			provider := Provider{RetryCount: numberPointer(test.Input.RetryCount), TimeoutMS: numberPointer(test.Input.TimeoutMS)}
			got, err := ProviderPolicy(provider)
			if test.Error != "" {
				if err == nil || err.Error() != test.Error {
					t.Fatalf("%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			budget, _ := ProviderOperationBudget(provider)
			same(t, map[string]any{"retryCount": got.RetryCount, "maxAttempts": got.MaxAttempts, "timeoutMs": got.TimeoutMS, "operationBudget": budget}, test.Value)
		})
	}
	var delays []struct {
		Attempt number
		After   *number
		Value   float64
	}
	decode(t, f["delays"], &delays)
	for i, test := range delays {
		if got := RetryDelay(float64(test.Attempt), numberPointer(test.After)); got != test.Value {
			t.Errorf("delay %d: %v != %v", i, got, test.Value)
		}
	}
	var headers []struct {
		Input *string
		Now   float64
		Value *float64
	}
	decode(t, f["retryHeaders"], &headers)
	for _, test := range headers {
		if got := ParseRetryAfter(test.Input, test.Now); !reflect.DeepEqual(got, test.Value) {
			t.Errorf("retry header %s: %s != %s", asJSON(test.Input), asJSON(got), asJSON(test.Value))
		}
	}
	var roles []struct{ Input, Value string }
	decode(t, f["roles"], &roles)
	for _, test := range roles {
		if got := CanonicalRole(test.Input); got != test.Value {
			t.Errorf("role: %s", got)
		}
	}
	var normalize []struct {
		Input []string
		Value []string
		Error string
	}
	decode(t, f["normalize"], &normalize)
	for _, test := range normalize {
		cfg := Config{}
		for _, role := range test.Input {
			cfg.Routes = append(cfg.Routes, Route{Role: role})
		}
		p, err := New(cfg, Options{})
		if test.Error != "" {
			if err == nil || err.Error() != test.Error {
				t.Fatalf("%v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, route := range p.config.Routes {
			got = append(got, route.Role)
		}
		if !reflect.DeepEqual(got, test.Value) {
			t.Fatalf("%v", got)
		}
	}
}
func TestRoutingOracle(t *testing.T) {
	var cases []struct {
		Name     string
		Config   Config
		Request  Request
		Outcomes map[string][]struct {
			Code             string
			Status           *int
			Retryable, Plain bool
			RetryAfterMS     *float64
		}
		Expected json.RawMessage
	}
	decode(t, fixture(t)["cases"], &cases)
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			events := []Event{}
			calls := []map[string]any{}
			counts := map[string]int{}
			p, err := New(test.Config, Options{Scheduler: scheduler.NewModelScheduler(), Sleep: func(context.Context, float64) error { return nil }, Observe: func(e Event) { events = append(events, e) }})
			var value any
			if err == nil {
				value, err = p.Run(context.Background(), test.Request, func(_ context.Context, a Attempt, settle func(any, error)) {
					id := a.Candidate.Model.ID
					counts[id]++
					calls = append(calls, map[string]any{"modelId": id, "role": a.Candidate.Role, "attempt": counts[id]})
					list := test.Outcomes[id]
					if counts[id] <= len(list) {
						next := list[counts[id]-1]
						if next.Plain {
							settle(nil, errors.New("private error"))
							return
						}
						if next.Code != "" {
							failure := NewError(next.Code, next.Status, next.Retryable)
							failure.RetryAfterMS = next.RetryAfterMS
							settle(nil, failure)
							return
						}
					}
					settle(id, nil)
				})
			}
			got := map[string]any{"events": events, "calls": calls}
			if err != nil {
				got["error"] = errorView(err)
			} else {
				got["modelId"] = value
			}
			same(t, got, test.Expected)
		})
	}
}

type fakeTimer struct {
	clock    *fakeClock
	at       float64
	callback func()
	active   bool
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := t.active
	t.active = false
	return was
}

type fakeClock struct {
	mu     sync.Mutex
	now    float64
	timers []*fakeTimer
}

func (c *fakeClock) Now() float64 { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) AfterFunc(ms float64, callback func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{c, c.now + ms, callback, true}
	c.timers = append(c.timers, timer)
	return timer
}
func (c *fakeClock) advance(at float64) {
	for {
		c.mu.Lock()
		c.now = at
		var selected *fakeTimer
		for _, timer := range c.timers {
			if timer.active && timer.at <= at && (selected == nil || timer.at < selected.at) {
				selected = timer
			}
		}
		if selected == nil {
			c.mu.Unlock()
			return
		}
		selected.active = false
		c.mu.Unlock()
		selected.callback()
	}
}
func (c *fakeClock) active() []bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := []bool{}
	for _, timer := range c.timers {
		result = append(result, timer.active)
	}
	return result
}
func eventually(t *testing.T, check func() bool) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for !check() {
		if time.Now().After(until) {
			t.Fatal("test coordination did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}
func baseConfig(protocol string, stage float64) Config {
	return Config{Providers: []Provider{{ID: "p", Protocol: protocol, RetryCount: n(0), TimeoutMS: n(100)}}, Models: []Model{{ID: "a", ProviderID: "p", Capabilities: []string{"text"}}}, Routes: []Route{{Role: "replyer", OrderedModelIDs: []string{"a"}, TimeoutMS: n(stage)}}}
}
func TestSchedulerLifecycleOracle(t *testing.T) {
	var cases []struct {
		Mode                  string
		Before, Result, After json.RawMessage
	}
	decode(t, fixture(t)["lifecycle"], &cases)
	for _, test := range cases {
		t.Run(test.Mode, func(t *testing.T) {
			clock := &fakeClock{}
			sched, _ := scheduler.New(1, 256, scheduler.Options{Clock: clock})
			var releaseBlock func(any, error)
			blockReady := make(chan struct{})
			var block *scheduler.Future
			if strings.HasSuffix(test.Mode, "_queue") {
				block = sched.SubmitModel(context.Background(), 0, func(_ context.Context, done func(any, error)) { releaseBlock = done; close(blockReady) })
				<-blockReady
			}
			native := strings.HasPrefix(test.Mode, "native")
			protocol := "oracle:live"
			stage := 1000.0
			if native {
				protocol = "openai-chat"
			}
			if test.Mode == "native_uncooperative" {
				stage = 30
			}
			cfg := baseConfig(protocol, stage)
			streaming := false
			cfg.Models[0].Streaming = &streaming
			p, _ := New(cfg, Options{Clock: clock, Scheduler: sched})
			entered := atomic.Bool{}
			call := make(chan func(any, error), 1)
			done := make(chan outcome, 1)
			go func() {
				v, e := p.Run(context.Background(), Request{Role: "replyer"}, func(_ context.Context, _ Attempt, settle func(any, error)) { entered.Store(true); call <- settle })
				done <- outcome{v, e}
			}()
			if block != nil {
				eventually(t, func() bool { return sched.Status().Queued == 1 })
			} else {
				eventually(t, entered.Load)
			}
			var before any
			var result outcome
			var settle func(any, error)
			if test.Mode == "native_queue" {
				clock.advance(130)
				before = map[string]any{"entered": entered.Load(), "status": sched.Status()}
				releaseBlock(nil, nil)
				block.Wait()
				settle = <-call
				settle("a", nil)
				result = <-done
			} else {
				if stage == 30 {
					clock.advance(30)
				} else {
					clock.advance(100)
				}
				result = <-done
				eventually(t, func() bool {
					want := 0
					if block != nil || native {
						want = 1
					}
					s := sched.Status()
					return s.Running == want && s.Queued == 0
				})
				before = map[string]any{"entered": entered.Load(), "status": sched.Status()}
				if block != nil {
					releaseBlock(nil, nil)
					block.Wait()
				}
				if entered.Load() {
					settle = <-call
					settle("a", nil)
				}
			}
			same(t, before, test.Before)
			view := map[string]any{}
			if result.err != nil {
				view["error"] = errorView(result.err)
			} else {
				view["modelId"] = result.value
			}
			same(t, view, test.Result)
			eventually(t, func() bool { return sched.Status().Running == 0 })
			same(t, sched.Status(), test.After)
		})
	}
}
func TestProgressOracle(t *testing.T) {
	var cases []struct {
		Mode                          string
		Active                        []bool
		StageAborted, ProviderAborted bool
		Reason                        *string
	}
	decode(t, fixture(t)["progress"], &cases)
	for _, test := range cases {
		t.Run(test.Mode, func(t *testing.T) {
			clock := &fakeClock{}
			parent, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			stage := newDeadline(parent, 200, "model_stage_timeout", clock)
			defer stage.Close()
			provider := newDeadline(stage.Context, 100, "provider_timeout", clock)
			defer provider.Close()
			if test.Mode == "progress" || test.Mode == "parent_abort" {
				provider.Progress()
			}
			if test.Mode == "dispose" {
				provider.Dispose()
				stage.Dispose()
			}
			if test.Mode == "expire" {
				clock.timers[1].callback()
			}
			if test.Mode == "parent_abort" {
				cancel(errors.New("caller"))
			}
			if !reflect.DeepEqual(clock.active(), test.Active) || (stage.Err() != nil) != test.StageAborted || (provider.Err() != nil) != test.ProviderAborted {
				t.Fatalf("timers %v, stage %v, provider %v", clock.active(), stage.Err(), provider.Err())
			}
			var reason *string
			if cause := context.Cause(provider.Context); cause != nil {
				text := cause.Error()
				reason = &text
			}
			if !reflect.DeepEqual(reason, test.Reason) {
				t.Fatalf("reason %v wanted %v", reason, test.Reason)
			}
		})
	}
}
