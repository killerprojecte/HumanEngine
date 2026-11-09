package observability

import "context"

// WaitPhysicalClose observes the raw logger's existing physical done boundary.
// It does not initiate Close, change counters/status, or infer physical liveness
// from Close's health/cancellation error. An already complete owner wins over a
// canceled context. This permits an owning app to release unrelated resources
// after health failure without confusing that failure with an outstanding tail.
func (l *Logger) WaitPhysicalClose(ctx context.Context) error {
	select {
	case <-l.done:
		return nil
	default:
	}
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
