package modelservice

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

type unpublishedProbe struct{ releases atomic.Int32 }

func (p *unpublishedProbe) Release() error { p.releases.Add(1); return nil }

// No timer fires. Stop cancels only when executeNative's deferred Close runs,
// after its final deadline.Err check but before attempts' ctx.Err check.
type cancelOnCloseClock struct{ cancel context.CancelFunc }

func (c cancelOnCloseClock) Now() float64 { return 0 }
func (c cancelOnCloseClock) AfterFunc(float64, func()) Timer {
	return &cancelOnCloseTimer{cancel: c.cancel}
}

type cancelOnCloseTimer struct {
	once   sync.Once
	cancel context.CancelFunc
}

func (t *cancelOnCloseTimer) Stop() bool { t.once.Do(t.cancel); return true }
func TestReleaseSuccessCancelledBetweenExecuteAndAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := baseConfig("openai-chat", 100)
	cfg.Routes[0].TimeoutMS = nil
	p, e := New(cfg, Options{Clock: cancelOnCloseClock{cancel}, Scheduler: scheduler.NewModelScheduler()})
	if e != nil {
		t.Fatal(e)
	}
	value := &unpublishedProbe{}
	result, e := p.attempts(ctx, Candidate{Role: "replyer", Model: cfg.Models[0], Provider: cfg.Providers[0]}, func(_ context.Context, _ Attempt, done func(any, error)) { done(value, nil) })
	if result != nil || e == nil || e.Error() != "request_cancelled" || value.releases.Load() != 1 {
		t.Fatalf("result=%v err=%v releases=%d", result, e, value.releases.Load())
	}
}
func TestReleaseSuccessCancelledBeforeCustomFutureWaitReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := baseConfig("fixture:custom", 100)
	cfg.Routes[0].TimeoutMS = nil
	p, e := New(cfg, Options{Scheduler: scheduler.NewModelScheduler()})
	if e != nil {
		t.Fatal(e)
	}
	policy, e := ProviderPolicy(cfg.Providers[0])
	if e != nil {
		t.Fatal(e)
	}
	value := &unpublishedProbe{}
	result, e := p.execute(ctx, Candidate{Role: "replyer", Model: cfg.Models[0], Provider: cfg.Providers[0]}, 1, policy, func(_ context.Context, _ Attempt, done func(any, error)) {
		done(value, nil) // Completion is queued, not dispatched inline.
		cancel()         // Starter has not returned, so future.Wait cannot yet receive success.
	})
	if result != nil || e == nil || e.Error() != "request_cancelled" || value.releases.Load() != 1 {
		t.Fatalf("result=%v err=%v releases=%d", result, e, value.releases.Load())
	}
}
