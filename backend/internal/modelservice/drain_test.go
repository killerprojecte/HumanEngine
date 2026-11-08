package modelservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
	"testing"
)

func TestPolicyDrainRetainsRunnerAfterRoleTimeout(t *testing.T) {
	clock := &fakeClock{}
	p, e := New(baseConfig("openai-chat", 100), Options{Clock: clock, Scheduler: scheduler.NewModelScheduler()})
	if e != nil {
		t.Fatal(e)
	}
	started := make(chan func(any, error), 1)
	returned := make(chan error, 1)
	go func() {
		_, err := p.Run(context.Background(), Request{Role: "replyer"}, func(_ context.Context, _ Attempt, done func(any, error)) { started <- done })
		returned <- err
	}()
	settle := <-started
	clock.advance(100)
	if e = <-returned; e == nil {
		t.Fatal("expected source role timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = p.Drain(ctx); !errors.Is(e, context.Canceled) {
		t.Fatalf("runner was not retained: %v", e)
	}
	settle(nil, errors.New("synthetic provider settled"))
	if e = p.Drain(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Run(context.Background(), Request{Role: "replyer"}, func(context.Context, Attempt, func(any, error)) {}); e == nil {
		t.Fatal("sealed policy accepted a request")
	}
}
