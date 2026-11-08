package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"sync"
)

// CloseAndWait preserves Close's cancellation behavior and additionally waits
// for accepted turns, timers and asynchronous callback owners. Producers must be
// stopped first. A canceled wait leaves ownership with the caller; it is safe to
// retry waiting, never to close the database on a failed wait.
// Model services must separately drain any work that outlives their settlement.
func (r *Runtime) CloseAndWait(ctx context.Context) error { r.Close(); return r.work.Wait(ctx) }

// trackedStart retains both invocation and callback execution. In particular a
// callback that settles synchronously does not let Wait return while its starter
// still has work to do. The service contract requires settlement after its actual
// asynchronous operation ends; cancellation alone cannot release this ownership.
func trackedStart[T any](r *Runtime, start func(func(T, error)), settle func(T, error)) {
	done, ok := r.work.Child()
	if !ok {
		var zero T
		settle(zero, code("runtime_closed"))
		return
	}
	var mu sync.Mutex
	returned, settled := false, false
	var resolveUncertainty func()
	finish := func(isReturn bool) {
		mu.Lock()
		if isReturn {
			returned = true
		} else {
			settled = true
			if resolveUncertainty != nil {
				resolveUncertainty()
				resolveUncertainty = nil
			}
		}
		ready := returned && settled
		mu.Unlock()
		if ready {
			done()
		}
	}
	// A starter panic proves only that the starter has returned. It cannot
	// prove whether an asynchronous callback was launched or is still running.
	// Keep callback ownership until actual settlement, even if it never arrives.
	// In that case waiting reports an explicit incomplete-start failure and never
	// authorizes releasing owners. A later real settlement permits a retry.
	defer func() {
		if failure := recover(); failure != nil {
			mu.Lock()
			if !settled {
				resolveUncertainty = r.work.MarkUncertain(code("runtime_callback_start_failed_pending_settlement"))
			}
			mu.Unlock()
			finish(true)
			panic(failure)
		}
		finish(true)
	}()

	var once sync.Once
	start(func(v T, e error) { once.Do(func() { defer finish(false); settle(v, e) }) })
}
func trackedWithin[T any](r *Runtime, ctx context.Context, start func(func(T, error))) (T, error) {
	return replyplan.WithinSignal(ctx, func(settle func(T, error)) { trackedStart(r, start, settle) })
}

// beginOperation admits externally entered work before it touches domain owners.
// Runtime-internal asynchronous children use trackedStart instead.
func (r *Runtime) beginOperation() (func(), bool) { return r.work.Start() }
