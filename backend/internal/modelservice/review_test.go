package modelservice

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

func TestReviewRetryAfterECMAScriptWhitespace(t *testing.T) {
	for _, test := range []struct {
		name, input string
		want        *float64
	}{
		{"byte_order_mark", "\uFEFF1\uFEFF", n(1000)},
		{"next_line_is_not_JS_whitespace", "\u00851\u0085", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ParseRetryAfter(&test.input, 0)
			if (got == nil) != (test.want == nil) || (got != nil && *got != *test.want) {
				t.Fatalf("ParseRetryAfter(%q) = %s, want %s", test.input, asJSON(got), asJSON(test.want))
			}
		})
	}
}

func TestReviewRetryAfterInjectedParserBoundary(t *testing.T) {
	text := "\uFEFF January 1, 2026 00:00:03 \uFEFF"
	calls := 0
	got := ParseRetryAfter(&text, 1000, func(value string) (float64, bool) {
		calls++
		if value != "January 1, 2026 00:00:03" {
			t.Errorf("parser got untrimmed input %q", value)
		}
		return 4000, true
	})
	if calls != 1 || got == nil || *got != 3000 {
		t.Fatalf("calls=%d got=%v", calls, got)
	}
	numeric := "1.5"
	got = ParseRetryAfter(&numeric, 1000, func(string) (float64, bool) { t.Error("numeric delta dispatched to date parser"); return 0, false })
	if got == nil || *got != 1500 {
		t.Fatalf("numeric delta = %v", got)
	}
	nonstandard := "2026-01-01T00:00:03Z"
	if got = ParseRetryAfter(&nonstandard, 0); got != nil {
		t.Fatal("default parser claims ISO Date.parse support")
	}
}

func TestReviewRoleBudgetRaceOracle(t *testing.T) {
	var cases []struct {
		Mode              string
		SettledBeforeTask bool
		Result            struct{ Error string }
	}
	decode(t, fixture(t)["roleBudgetRaces"], &cases)
	if len(cases) != 2 {
		t.Fatal("missing source role-budget race evidence")
	}
	for _, test := range cases {
		t.Run(test.Mode, func(t *testing.T) {
			previous := runtime.GOMAXPROCS(1)
			defer runtime.GOMAXPROCS(previous)
			clock := &fakeClock{}
			cfg := baseConfig("openai-chat", 50)
			sched := scheduler.NewModelScheduler()
			p, err := New(cfg, Options{Clock: clock, Scheduler: sched})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan func(any, error), 1)
			result := make(chan error, 1)
			go func() {
				_, err := p.Run(ctx, Request{Role: "replyer"}, func(_ context.Context, _ Attempt, settle func(any, error)) { started <- settle })
				result <- err
			}()
			settle := <-started
			// Run both events in this goroutine before the result waiter resumes.
			// The source linearizes expiry in the synchronous abort listener.
			if test.Mode == "timeout_then_parent_abort" {
				clock.advance(50)
				cancel()
			} else {
				cancel()
				clock.advance(50)
			}
			var returned error
			settledBeforeTask := false
			select {
			case returned = <-result:
				settledBeforeTask = true
			case <-time.After(50 * time.Millisecond):
			}
			if settledBeforeTask != test.SettledBeforeTask {
				t.Errorf("settled before native task = %v, source = %v", settledBeforeTask, test.SettledBeforeTask)
			}
			if sched.Status().Running != 1 {
				t.Error("native task lost its slot before actual settlement")
			}
			settle(nil, context.Canceled)
			if !settledBeforeTask {
				returned = <-result
			}
			if returned == nil || returned.Error() != test.Result.Error {
				t.Errorf("got %v, want %s", returned, test.Result.Error)
			}
			eventually(t, func() bool { return sched.Status().Running == 0 })
		})
	}
}

func TestReviewSuccessfulRunDisposesTimersWithoutCancellingContext(t *testing.T) {
	clock := &fakeClock{}
	p, err := New(baseConfig("openai-chat", 50), Options{Clock: clock, Scheduler: scheduler.NewModelScheduler()})
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var retained context.Context
	value, err := p.Run(parent, Request{Role: "replyer"}, func(ctx context.Context, _ Attempt, settle func(any, error)) {
		retained = ctx
		settle("ok", nil)
	})
	if err != nil || value != "ok" {
		t.Fatalf("value=%v error=%v", value, err)
	}
	clock.advance(1000)
	if retained.Err() != nil {
		t.Fatalf("settlement aborted retained context: %v", retained.Err())
	}
	cancel()
	if retained.Err() == nil {
		t.Fatal("settlement detached parent cancellation")
	}
}
