package modelservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

func TestNativeStreamPolicyOracle(t *testing.T) {
	var cases []struct {
		Mode           string
		Calls          int
		Result, Status []byte
	}
	// RawMessage is required for an object rather than base64 []byte decoding.
	var rows []map[string]any
	decode(t, fixture(t)["streams"], &rows)
	for _, row := range rows {
		cases = append(cases, struct {
			Mode           string
			Calls          int
			Result, Status []byte
		}{row["mode"].(string), int(row["calls"].(float64)), asJSON(row["result"]), asJSON(row["status"])})
	}
	for _, test := range cases {
		t.Run(test.Mode, func(t *testing.T) {
			clock := &fakeClock{}
			sched := scheduler.NewModelScheduler()
			cfg := baseConfig("openai-chat", 100)
			cfg.Providers[0].RetryCount = n(2)
			fallback := cfg.Models[0]
			fallback.ID = "b"
			cfg.Models = append(cfg.Models, fallback)
			cfg.Routes[0].OrderedModelIDs = append(cfg.Routes[0].OrderedModelIDs, "b")
			p, _ := New(cfg, Options{Clock: clock, Scheduler: sched})
			calls := atomic.Int32{}
			started := make(chan func(any, error), 1)
			done := make(chan outcome, 1)
			go func() {
				v, e := p.Run(context.Background(), Request{Role: "replyer"}, func(_ context.Context, a Attempt, settle func(any, error)) {
					calls.Add(1)
					a.FirstContent()
					started <- settle
				})
				done <- outcome{v, e}
			}()
			settle := <-started
			if test.Mode == "progress_survives_deadlines" {
				clock.advance(140)
				settle("a", nil)
			} else if test.Mode == "partial_is_terminal" {
				settle(nil, NewError("provider_stream_interrupted", nil, false))
			} else {
				settle(nil, NewError("incomplete_model_response", nil, false))
			}
			result := <-done
			view := map[string]any{}
			if result.err != nil {
				view["error"] = errorView(result.err)
			} else {
				view["modelId"] = result.value
			}
			same(t, view, test.Result)
			if int(calls.Load()) != test.Calls {
				t.Fatal(calls.Load())
			}
			eventually(t, func() bool { return sched.Status().Running == 0 })
			same(t, sched.Status(), test.Status)
		})
	}
}

// This is a test-only invocation, not a production adapter. Real HTTP requests
// go only to this in-process loopback server, with no credentials or external IO.
func TestLocalHTTPRetryAndFallback(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := received.Add(1)
		if call == 1 {
			w.Header().Set("Retry-After", "0.8")
			w.WriteHeader(429)
			return
		}
		if call == 2 {
			w.WriteHeader(503)
			return
		}
		if call == 3 {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, "complete")
	}))
	defer server.Close()
	clock := &fakeClock{}
	sched := scheduler.NewModelScheduler()
	cfg := baseConfig("openai-chat", 10000)
	cfg.Providers[0].RetryCount = n(2)
	cfg.Models = append(cfg.Models, Model{ID: "b", ProviderID: "p", Capabilities: []string{"text"}})
	cfg.Routes[0].OrderedModelIDs = append(cfg.Routes[0].OrderedModelIDs, "b")
	waits := []float64{}
	p, _ := New(cfg, Options{Clock: clock, Scheduler: sched, Sleep: func(_ context.Context, ms float64) error { waits = append(waits, ms); return nil }})
	value, err := p.Run(context.Background(), Request{Role: "thinker"}, func(ctx context.Context, a Attempt, settle func(any, error)) {
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			res, err := server.Client().Do(req)
			if err != nil {
				settle(nil, err)
				return
			}
			defer res.Body.Close()
			if res.StatusCode >= 400 {
				header := res.Header.Get("Retry-After")
				settle(nil, HTTPFailure(res.StatusCode, ParseRetryAfter(&header, clock.Now())))
				return
			}
			body, err := io.ReadAll(res.Body)
			if err != nil {
				settle(nil, err)
				return
			}
			settle(a.Candidate.Model.ID+":"+string(body), nil)
		}()
	})
	if err != nil || value != "b:complete" || received.Load() != 4 || !reflect.DeepEqual(waits, []float64{800, 500}) {
		t.Fatalf("value=%v err=%v calls=%v waits=%v", value, err, received.Load(), waits)
	}
}

func TestArbitraryWrappedFailureNeverRetries(t *testing.T) {
	cfg := baseConfig("plugin:pending-sdk", 1000)
	cfg.Providers[0].RetryCount = n(5)
	for _, failure := range []error{errors.New("ECONNRESET"), fmt.Errorf("outer: %w", HTTPFailure(503, nil)), &lookalike{}} {
		p, _ := New(cfg, Options{Scheduler: scheduler.NewModelScheduler()})
		calls := 0
		_, err := p.Run(context.Background(), Request{Role: "replyer"}, func(_ context.Context, _ Attempt, settle func(any, error)) { calls++; settle(nil, failure) })
		model, ok := err.(*ModelError)
		if !ok || model.Code != "all_models_failed" || model.Retryable || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
		if !errors.Is(err, failure) {
			t.Fatal("private failure identity lost")
		}
	}
}

type lookalike struct{}

func (*lookalike) Error() string   { return "provider_http_503" }
func (*lookalike) Retryable() bool { return true }

func TestNativeNetworkAllowlist(t *testing.T) {
	for _, code := range []string{"ECONNRESET", "ECONNREFUSED", "ETIMEDOUT", "EAI_AGAIN", "ENETUNREACH", "EHOSTUNREACH", "EPIPE", "UND_ERR_CONNECT_TIMEOUT", "UND_ERR_SOCKET"} {
		if !BuiltinNetworkFailure(code, "", nil).Retryable || !BuiltinNetworkFailure("", code, nil).Retryable {
			t.Fatal(code)
		}
	}
	if BuiltinNetworkFailure("fetch failed", "private: ECONNRESET", nil).Retryable {
		t.Fatal("arbitrary error acquired retry eligibility")
	}
}

func TestCancellationDuringBackoffStopsFallback(t *testing.T) {
	cfg := baseConfig("openai-chat", 1000)
	cfg.Providers[0].RetryCount = n(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	p, _ := New(cfg, Options{Scheduler: scheduler.NewModelScheduler(), Sleep: func(ctx context.Context, ms float64) error { cancel(); return nil }})
	_, err := p.Run(ctx, Request{Role: "replyer"}, func(_ context.Context, _ Attempt, settle func(any, error)) {
		calls++
		settle(nil, HTTPFailure(503, nil))
	})
	if err == nil || err.Error() != "request_cancelled" || calls != 1 || ProviderAttemptsHandled(err) {
		t.Fatalf("%v %d", err, calls)
	}
}
func TestNativeProviderTimeoutNotRetried(t *testing.T) {
	cfg := baseConfig("openai-chat", 1000)
	cfg.Providers[0].RetryCount = n(5)
	clock := &fakeClock{}
	p, _ := New(cfg, Options{Clock: clock, Scheduler: scheduler.NewModelScheduler()})
	ready := make(chan struct{})
	calls := atomic.Int32{}
	result := make(chan error, 1)
	go func() {
		_, err := p.Run(context.Background(), Request{Role: "replyer"}, func(ctx context.Context, _ Attempt, settle func(any, error)) {
			calls.Add(1)
			context.AfterFunc(ctx, func() { settle(nil, ctx.Err()) })
			close(ready)
		})
		result <- err
	}()
	<-ready
	clock.advance(100)
	err := <-result
	if calls.Load() != 1 || err.(*ModelError).Failures()[0].Error() != "provider_timeout" || err.(*ModelError).Retryable {
		t.Fatalf("%v, %v", calls.Load(), err)
	}
}
func TestStageDeadlineIncludesRetryWait(t *testing.T) {
	cfg := baseConfig("openai-chat", 100)
	cfg.Providers[0].RetryCount = n(2)
	clock := &fakeClock{}
	waiting := make(chan struct{})
	p, _ := New(cfg, Options{Clock: clock, Scheduler: scheduler.NewModelScheduler(), Observe: func(e Event) {
		if e.Kind == "retry" {
			close(waiting)
		}
	}})
	done := make(chan error, 1)
	calls := atomic.Int32{}
	go func() {
		_, err := p.Run(context.Background(), Request{Role: "replyer"}, func(_ context.Context, _ Attempt, settle func(any, error)) {
			calls.Add(1)
			settle(nil, HTTPFailure(503, nil))
		})
		done <- err
	}()
	<-waiting
	clock.advance(100)
	err := <-done
	if err.Error() != "model_stage_timeout" || calls.Load() != 1 {
		t.Fatalf("%v calls=%d", err, calls.Load())
	}
}
func TestStreamProgressPreservesParentCancellation(t *testing.T) {
	cfg := baseConfig("openai-chat", 1000)
	clock := &fakeClock{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, _ := New(cfg, Options{Clock: clock, Scheduler: scheduler.NewModelScheduler()})
	ready := make(chan context.Context, 1)
	settlements := make(chan func(any, error), 1)
	done := make(chan error, 1)
	go func() {
		_, err := p.Run(ctx, Request{Role: "replyer"}, func(attemptCtx context.Context, a Attempt, settle func(any, error)) {
			a.FirstContent()
			ready <- attemptCtx
			settlements <- settle
		})
		done <- err
	}()
	attemptCtx := <-ready
	clock.advance(2000)
	if attemptCtx.Err() != nil {
		t.Fatal("progress did not stop managed deadlines")
	}
	cancel()
	if attemptCtx.Err() == nil {
		t.Fatal("caller cancellation lost")
	}
	(<-settlements)("late success", nil)
	if err := <-done; err.Error() != "request_cancelled" {
		t.Fatal(err)
	}
}
func TestCustomAndNonstreamProgressCannotDisableBudgets(t *testing.T) {
	for _, protocol := range []string{"plugin:custom", "openai-chat"} {
		t.Run(protocol, func(t *testing.T) {
			cfg := baseConfig(protocol, 1000)
			streaming := false
			cfg.Models[0].Streaming = &streaming
			clock := &fakeClock{}
			sched := scheduler.NewModelScheduler()
			p, _ := New(cfg, Options{Clock: clock, Scheduler: sched})
			ready := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, err := p.Run(context.Background(), Request{Role: "replyer"}, func(ctx context.Context, a Attempt, settle func(any, error)) {
					a.FirstContent()
					context.AfterFunc(ctx, func() { settle(nil, ctx.Err()) })
					close(ready)
				})
				done <- err
			}()
			<-ready
			clock.advance(100)
			err := <-done
			if err.(*ModelError).Failures()[0].Error() != "provider_timeout" {
				t.Fatal(err)
			}
		})
	}
}
func TestConfigAndInvocationIsolation(t *testing.T) {
	cfg := baseConfig("openai-chat", 1000)
	p, _ := New(cfg, Options{Scheduler: scheduler.NewModelScheduler()})
	cfg.Models[0].Capabilities[0] = "embedding"
	*cfg.Providers[0].TimeoutMS = 0
	cfg.Routes[0].OrderedModelIDs[0] = "missing"
	for i := 0; i < 2; i++ {
		value, err := p.Run(context.Background(), Request{Role: "thinker"}, func(_ context.Context, a Attempt, settle func(any, error)) {
			a.Candidate.Model.Capabilities[0] = "embedding"
			*a.Candidate.Provider.TimeoutMS = 0
			settle(a.Candidate.Role, nil)
		})
		if value != "replyer" || err != nil {
			t.Fatalf("%v %v", value, err)
		}
	}
	for _, cap := range []float64{0, 0.5, 1000001} {
		cfg.NativeMaxOutputTokens = n(cap)
		if _, err := New(cfg, Options{}); err == nil || err.Error() != "invalid_output_cap" {
			t.Fatalf("cap=%v err=%v", cap, err)
		}
	}
}
func TestModelErrorPublicSerialization(t *testing.T) {
	secret := errors.New("private credential")
	err := withCause(NewError("all_models_failed", nil, false), secret)
	err.failures = []error{secret}
	if strings.Contains(string(asJSON(err)), "private") || strings.Contains(fmt.Sprint(err), "private") {
		t.Fatal("private failure leaked")
	}
}
func TestSharedSchedulerAndPriorityScope(t *testing.T) {
	sched, _ := scheduler.New(1, 256, scheduler.Options{})
	release := make(chan func(any, error), 1)
	blocker := sched.SubmitModel(context.Background(), 0, func(_ context.Context, done func(any, error)) { release <- done })
	finish := <-release
	cfg := baseConfig("openai-chat", 1000)
	p, _ := New(cfg, Options{Scheduler: sched})
	background, _ := scheduler.WithModelPriority(context.Background(), 2)
	scopes := make(chan float64, 2)
	done := make(chan error, 2)
	for _, ctx := range []context.Context{background, context.Background()} {
		go func() {
			_, err := p.Run(ctx, Request{Role: "replyer"}, func(ctx context.Context, _ Attempt, settle func(any, error)) {
				scopes <- scheduler.EffectiveModelPriority(ctx, 0)
				settle("ok", nil)
			})
			done <- err
		}()
	}
	eventually(t, func() bool { return sched.Status().Queued == 2 })
	finish(nil, nil)
	blocker.Wait()
	if first, second := <-scopes, <-scopes; first != 0 || second != 2 {
		t.Fatalf("priorities %v, %v", first, second)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
