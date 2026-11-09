package observability

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
	"time"
)

// ErrDeliveryIncomplete is a completed physical close with degraded delivery,
// not an outstanding owner. An owning app must still release its database.
var ErrDeliveryIncomplete = errors.New("log_delivery_incomplete")
var errLogCloseFailed = errors.New("log_close_failed")

type settledLogClose struct {
	result *logdelivery.FlushResult
	err    error
}

func (s settledLogClose) Wait(context.Context) (*logdelivery.FlushResult, error) {
	return s.result, s.err
}
func beginLogClose(lease logLease) (operation logClose) {
	if lease == nil {
		return settledLogClose{}
	}
	defer func() {
		if recover() != nil {
			operation = settledLogClose{err: errLogCloseFailed}
		}
	}()
	return lease.beginClose()
}
func awaitLogClose(operation logClose) (result *logdelivery.FlushResult, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = errLogCloseFailed
		}
	}()
	return operation.Wait(context.Background())
}

type storeCloseRound struct {
	done    chan struct{}
	results []logdelivery.FlushResult
	err     error
}

func (l *StoreLogger) beginCloseLogs() *storeCloseRound {
	l.mu.Lock()
	l.closed = true
	if l.closeInitiated == nil {
		l.closeInitiated = make(chan struct{})
	}
	idle := l.admissionsIdle
	l.mu.Unlock()
	round := &storeCloseRound{done: make(chan struct{}), results: []logdelivery.FlushResult{}}
	go func() {
		defer close(round.done)
		<-idle
		l.mu.Lock()
		file, out := l.file, l.stdout
		l.mu.Unlock()
		// Begin in source array order, then await both even if either fails.
		operations := []logClose{beginLogClose(file), beginLogClose(out)}
		l.markCloseInitiated()
		complete := true
		for _, op := range operations {
			result, err := awaitLogClose(op)
			if err != nil {
				round.err = errLogCloseFailed
				complete = false
			}
			if result != nil {
				round.results = append(round.results, *result)
				complete = complete && result.Complete
			}
		}
		l.mu.Lock()
		complete = complete && (l.shutdownComplete == nil || *l.shutdownComplete) && !l.initializationFailed && l.unserializable == 0 && l.closedRejected == 0
		l.shutdownComplete = &complete
		l.mu.Unlock()
	}()
	return round
}
func (l *StoreLogger) CloseLogs(ctx context.Context) ([]logdelivery.FlushResult, error) {
	round := l.beginCloseLogs()
	select {
	case <-round.done:
		return round.results, round.err
	default:
	}
	select {
	case <-round.done:
		return round.results, round.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close joins the source logical operation, then only the physical final-release
// tails owned by these leases. Context cancellation never relinquishes them.
func (l *StoreLogger) Close(ctx context.Context) error {
	_, logicalErr := l.CloseLogs(ctx)
	if errors.Is(logicalErr, context.Canceled) || errors.Is(logicalErr, context.DeadlineExceeded) {
		return logicalErr
	}
	if physicalErr := l.WaitPhysicalClose(ctx); physicalErr != nil {
		return physicalErr
	}
	complete := l.CloseLogsSync(logdelivery.DefaultTimeout)
	if logicalErr != nil || !complete {
		return ErrDeliveryIncomplete
	}
	return nil
}

// CloseLogsSync preserves Store.close's file-then-stdout shared deadline and
// sticky health. In Go, an already admitted concurrent producer may still be
// acquiring its leases (an interleaving impossible in single-threaded source).
// Its remaining budget bounds this wait too: on expiry, return incomplete and
// transfer the admitted tail to an independent close round. Physical ownership
// is retained until WaitPhysicalClose observes release and required Done.
// The application owns diagnostic/database cleanup afterward.
func (l *StoreLogger) CloseLogsSync(timeout time.Duration) bool {
	deadline := l.now().Add(timeout)
	l.mu.Lock()
	l.closed = true
	if l.closeInitiated == nil {
		l.closeInitiated = make(chan struct{})
	}
	idle := l.admissionsIdle
	l.mu.Unlock()
	if !waitStoreAdmission(idle, max(time.Duration(0), deadline.Sub(l.now()))) {
		l.mu.Lock()
		complete := false
		l.shutdownComplete = &complete
		l.mu.Unlock()
		l.beginCloseLogs() // owns the admitted setup and eventual physical releases
		return false
	}
	l.mu.Lock()
	leases := []logLease{l.file, l.stdout}
	complete := (l.shutdownComplete == nil || *l.shutdownComplete) && !l.initializationFailed && l.unserializable == 0 && l.closedRejected == 0
	l.mu.Unlock()
	for _, lease := range leases {
		if lease == nil {
			continue
		}
		func() {
			defer func() {
				if recover() != nil {
					complete = false
				}
			}()
			result := lease.CloseSync(max(time.Duration(0), deadline.Sub(l.now())))
			if result != nil && !result.Complete {
				complete = false
			}
		}()
	}
	l.markCloseInitiated()
	l.mu.Lock()
	complete = complete && (l.shutdownComplete == nil || *l.shutdownComplete)
	l.shutdownComplete = &complete
	l.mu.Unlock()
	return complete
}

// WaitPhysicalClose reports ownership completion independently from Close's
// health/error result. It never infers liveness from a cancellation error, and
// never starts a close, acquires a lease, or waits another Store's nonlast lease.
func (l *StoreLogger) WaitPhysicalClose(ctx context.Context) error {
	l.mu.Lock()
	initiated := l.closeInitiated
	l.mu.Unlock()
	if initiated == nil {
		return logdelivery.ErrLeaseNotReleased
	}
	select {
	case <-initiated:
	default:
		select {
		case <-initiated:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	l.mu.Lock()
	leases := []logLease{l.file, l.stdout}
	l.mu.Unlock()
	var result error
	for _, lease := range leases {
		if lease != nil {
			result = errors.Join(result, lease.WaitPhysicalRelease(ctx))
		}
	}
	return result
}
func (l *StoreLogger) markCloseInitiated() {
	l.mu.Lock()
	if l.closeInitiated == nil {
		l.closeInitiated = make(chan struct{})
	}
	initiated := l.closeInitiated
	l.mu.Unlock()
	l.closeInitiatedOnce.Do(func() { close(initiated) })
}

func waitStoreAdmission(idle <-chan struct{}, remaining time.Duration) bool {
	select {
	case <-idle:
		return true
	default:
	}
	if remaining <= 0 {
		return false
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}
