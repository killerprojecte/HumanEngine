package modelservice

import (
	"context"
	"math"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

type Timer = scheduler.Timer
type Clock interface {
	Now() float64
	AfterFunc(delayMS float64, callback func()) Timer
}
type progressKey struct{}

// progressDeadline retains parent cancellation when meaningful native content
// stops both provider and role timers. Dispose removes timers but does not abort
// contexts captured by an executor or an opaque continuation owner.
type progressDeadline struct {
	context.Context
	cancel     context.CancelCauseFunc
	mu         sync.Mutex
	timer      Timer
	stopped    bool
	disposed   bool
	stopParent func() bool
	parent     context.Context
}

func newDeadline(parent context.Context, ms float64, code string, clock Clock) *progressDeadline {
	return newDeadlineWithTimeout(parent, ms, code, clock, nil)
}

// onTimeout runs under mu only when this timer wins cancellation. Role result
// settlement uses the same lock, so the first settled outcome cannot be replaced
// by a later parent abort or by a nondeterministic select of ready channels.
func newDeadlineWithTimeout(parent context.Context, ms float64, code string, clock Clock, onTimeout func(error)) *progressDeadline {
	ctx, cancel := context.WithCancelCause(parent)
	d := &progressDeadline{Context: ctx, cancel: cancel, parent: parent}
	d.Context = context.WithValue(ctx, progressKey{}, d)
	d.mu.Lock()
	d.timer = clock.AfterFunc(ms, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if !d.stopped && !d.disposed {
			cause := NewError(code, nil, false)
			d.cancel(cause)
			// Context cause preserves the first cancellation, including a parent
			// cancellation that won while this timer was entering its callback.
			if context.Cause(d.Context) == cause && onTimeout != nil {
				onTimeout(cause)
			}
		}
	})
	d.mu.Unlock()
	d.stopParent = context.AfterFunc(parent, d.Dispose)
	if parent.Err() != nil {
		d.Dispose()
	}
	return d
}
func (d *progressDeadline) Progress() {
	d.mu.Lock()
	if d.disposed || d.Err() != nil {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.mu.Unlock()
	if p, ok := d.parent.Value(progressKey{}).(*progressDeadline); ok {
		p.Progress()
	}
}
func (d *progressDeadline) disposeLocked() {
	d.disposed = true
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
func (d *progressDeadline) Dispose() {
	d.mu.Lock()
	d.disposeLocked()
	d.mu.Unlock()
	// stopParent is assigned once during construction. Parent callbacks only
	// dispose the timer; callers detach their callback separately with Close.
}
func (d *progressDeadline) Close() {
	d.Dispose()
	if d.stopParent != nil {
		d.stopParent()
	}
}
func waitRetry(ctx context.Context, ms float64, clock Clock) error {
	if ctx.Err() != nil {
		return cancelled(context.Cause(ctx))
	}
	if math.IsNaN(ms) || math.IsInf(ms, 0) {
		ms = 10000
	} else {
		ms = math.Min(10000, math.Max(0, ms))
	}
	done := make(chan struct{})
	timer := clock.AfterFunc(ms, func() { close(done) })
	defer timer.Stop()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if ctx.Err() != nil {
		return cancelled(context.Cause(ctx))
	}
	return nil
}
