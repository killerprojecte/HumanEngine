package recovery

import (
	"context"
	"sync"
)

// Native SQLite reservations handle processes; this cancellable connection
// mutex prevents concurrent statements on the one connection within a process.
type authorityLock struct {
	once sync.Once
	held chan struct{}
}

func (m *authorityLock) LockContext(ctx context.Context) error {
	m.once.Do(func() { m.held = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.held <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *authorityLock) Lock()   { _ = m.LockContext(context.Background()) }
func (m *authorityLock) Unlock() { <-m.held }
