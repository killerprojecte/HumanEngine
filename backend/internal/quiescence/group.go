// Package quiescence tracks actual completion separately from cancellation.
package quiescence

import (
	"context"
	"errors"
	"sync"
)

// Group is zero-value ready. Seal and Start share one lock: Wait can never
// observe an empty group immediately before an already accepted registration.
// Child is only for work launched synchronously by an existing counted owner.
type Group struct {
	mu        sync.Mutex
	active    int
	sealed    bool
	empty     chan struct{}
	changed   chan struct{}
	uncertain map[*uncertainty]struct{}
}

func (g *Group) Start() (func(), bool) { return g.start(false) }
func (g *Group) Child() (func(), bool) { return g.start(true) }
func (g *Group) start(child bool) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed && (!child || g.active == 0) {
		return nil, false
	}
	if g.active == 0 {
		g.empty = make(chan struct{})
	}
	g.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active--
			if g.active == 0 {
				close(g.empty)
			}
		})
	}, true
}
func (g *Group) Seal() { g.mu.Lock(); g.sealed = true; g.mu.Unlock() }

// MarkUncertain reports an accepted owner's abnormal starter outcome without
// decrementing its physical work count. Wait reports it promptly, including with
// Background. Resolve only after actual callback settlement is established.
type uncertainty struct{ err error }

func (g *Group) MarkUncertain(err error) func() {
	if err == nil {
		err = errors.New("work_completion_uncertain")
	}
	token := &uncertainty{err: err}
	g.mu.Lock()
	if g.uncertain == nil {
		g.uncertain = map[*uncertainty]struct{}{}
	}
	g.uncertain[token] = struct{}{}
	g.notifyLocked()
	g.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); delete(g.uncertain, token); g.notifyLocked(); g.mu.Unlock() }) }
}
func (g *Group) notifyLocked() {
	if g.changed != nil {
		close(g.changed)
	}
	g.changed = make(chan struct{})
}
func (g *Group) Wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		g.sealed = true
		if len(g.uncertain) > 0 {
			failures := make([]error, 0, len(g.uncertain))
			for failure := range g.uncertain {
				failures = append(failures, failure.err)
			}
			g.mu.Unlock()
			return errors.Join(failures...)
		}
		if g.active == 0 {
			g.mu.Unlock()
			return nil
		}
		if g.changed == nil {
			g.changed = make(chan struct{})
		}
		done, changed := g.empty, g.changed
		g.mu.Unlock()
		select {
		case <-done:
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
