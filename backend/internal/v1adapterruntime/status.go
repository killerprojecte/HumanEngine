package v1adapterruntime

import (
	"context"
	"sync/atomic"
)

// adapterStatusOwner belongs to one runtime wrapper. Disposal publishes the
// source wrapper's terminal disconnected value; a new generation cannot supply
// this object's state. Host reads are raw: Runtime owns membership/two-read logic.
type adapterStatusOwner struct {
	disposed atomic.Bool
	read     func(context.Context) (string, error)
	cancel   context.CancelFunc
}

func (s *adapterStatusOwner) status(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.disposed.Load() {
		return "disconnected", nil
	}
	value, err := s.read(ctx)
	if s.disposed.Load() {
		return "disconnected", nil
	}
	return value, err
}
func (s *adapterStatusOwner) dispose() {
	if !s.disposed.Swap(true) {
		s.cancel()
	}
}
