//go:build web_read_candidate

package webreadruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Controlled host proves lifecycle ordering only. policy_test.go separately
// executes the actual default/candidate WASM and actual source fixtures.
type transientTestHost struct {
	mu                             sync.Mutex
	enableCount, disableCount      int
	enableErr, bindErr, disableErr error
	call                           func(context.Context, context.Context, []byte) ([]byte, error)
	draining                       chan struct{}
	release                        <-chan struct{}
}

func (h *transientTestHost) Enable(context.Context, string, []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enableCount++
	return h.enableErr
}
func (h *transientTestHost) BindCallWithAdmission(string) (func(context.Context, context.Context, []byte) ([]byte, error), error) {
	return h.call, h.bindErr
}
func (h *transientTestHost) Disable(ctx context.Context, _ string) error {
	if ctx.Err() != nil {
		return errors.New("cleanup_borrowed_cancelled_context")
	}
	if h.draining != nil {
		h.draining <- struct{}{}
	}
	if h.release != nil {
		<-h.release
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disableCount++
	return h.disableErr
}
func (h *transientTestHost) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enableCount, h.disableCount
}
func policySuccess(context.Context, context.Context, []byte) ([]byte, error) {
	return []byte(`{"valid":true}`), nil
}

func TestTransientPolicyCompletionWaitsForPhysicalDisable(t *testing.T) {
	for _, validationErr := range []error{nil, errors.New("synthetic_validation_failed")} {
		t.Run(func() string {
			if validationErr == nil {
				return "success"
			}
			return "failure"
		}(), func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			resume := func() { once.Do(func() { close(release) }) }
			defer resume()
			h := &transientTestHost{call: func(context.Context, context.Context, []byte) ([]byte, error) {
				if validationErr != nil {
					return nil, validationErr
				}
				return []byte(`{"valid":true}`), nil
			}, draining: make(chan struct{}, 1), release: release}
			p := transientPolicy(h, nil)
			finished := make(chan error, 1)
			go func() { finished <- p.Validate(context.Background(), new(jsjson.Object)) }()
			select {
			case <-h.draining:
			case <-time.After(time.Second):
				t.Fatal("physical Disable was not entered")
			}
			select {
			case err := <-finished:
				t.Fatal("Validate returned before physical Disable", err)
			default:
			}
			cancelled, stop := context.WithCancel(context.Background())
			stop()
			if err := p.Validate(cancelled, new(jsjson.Object)); !errors.Is(err, context.Canceled) {
				t.Fatal("queued cancellation", err)
			}
			enabled, disabled := h.counts()
			if enabled != 1 || disabled != 0 {
				t.Fatal("cancelled waiter stole active physical owner", enabled, disabled)
			}
			resume()
			select {
			case err := <-finished:
				if !errors.Is(err, validationErr) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("physical completion did not settle Validate")
			}
			enabled, disabled = h.counts()
			if enabled != 1 || disabled != 1 {
				t.Fatal(enabled, disabled)
			}
		})
	}
}
func TestTransientPolicyPreservesAdmissionAndCleanupFailures(t *testing.T) {
	bindErr, disableErr, enableErr := errors.New("bind"), errors.New("disable"), errors.New("enable")
	h := &transientTestHost{call: policySuccess, bindErr: bindErr, disableErr: disableErr}
	err := transientPolicy(h, nil).Validate(context.Background(), new(jsjson.Object))
	if !errors.Is(err, bindErr) || !errors.Is(err, disableErr) {
		t.Fatal("lost setup/cleanup failure", err)
	}
	h = &transientTestHost{enableErr: enableErr, call: policySuccess}
	if err = transientPolicy(h, nil).Validate(context.Background(), new(jsjson.Object)); !errors.Is(err, enableErr) {
		t.Fatal(err)
	}
	enabled, disabled := h.counts()
	if enabled != 1 || disabled != 0 {
		t.Fatal("failed admission disabled another owner", enabled, disabled)
	}
}
func TestTransientPolicyAdmittedSynchronousValidationStillDrainsAfterCallerCancel(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	defer resume()
	h := &transientTestHost{call: func(wait, life context.Context, _ []byte) ([]byte, error) {
		close(entered)
		<-release
		if life.Err() != nil {
			return nil, errors.New("admitted_source_policy_was_interrupted")
		}
		return []byte(`{"valid":true}`), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := transientPolicy(h, nil)
	finished := make(chan error, 1)
	go func() { finished <- p.Validate(ctx, new(jsjson.Object)) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("validation did not enter")
	}
	cancel()
	select {
	case err := <-finished:
		t.Fatal("caller cancellation falsely completed admitted source validation", err)
	default:
	}
	resume()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("validation did not physically finish")
	}
	enabled, disabled := h.counts()
	if enabled != 1 || disabled != 1 {
		t.Fatal("late synchronous validation did not unload", enabled, disabled)
	}
}
