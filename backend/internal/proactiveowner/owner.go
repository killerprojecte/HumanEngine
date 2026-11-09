// Package proactiveowner owns Engine's 60-second proactive producer.
package proactiveowner

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
)

const Interval = 60 * time.Second

type Scene struct {
	ID, Kind         string
	ProactiveEnabled bool
}

// Stop prevents future timer admission. A callback already in flight need not
// return before Stop does; Owner separately tracks its actual resource use.
type Stopper interface{ Stop() }

// Every must not call f before the first interval expires, and must serialize
// timer callbacks. Proactive tasks returned by those callbacks may overlap.
type Clock interface {
	Every(time.Duration, func()) Stopper
}
type Config struct {
	Scenes func() ([]Scene, error)
	// Proactive starts the real runtime operation promptly. Its completion channel
	// must close after that operation settles, not merely after cancellation.
	Proactive func(string, bool) (<-chan struct{}, error)
	// Source discards the Promise without catching rejection. Go requires an
	// explicit host error sink instead of silently swallowing invocation errors.
	OnError func(error)
	Clock   Clock
}
type Owner struct {
	mu              sync.Mutex
	cfg             Config
	started, closed bool
	timer           Stopper
	work            quiescence.Group
}

func New(cfg Config) (*Owner, error) {
	if cfg.Scenes == nil || cfg.Proactive == nil || cfg.OnError == nil {
		return nil, errors.New("proactive_owner_dependencies_required")
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	return &Owner{cfg: cfg}, nil
}

// Start belongs after successful plugin startup, exactly where Engine installs
// its interval. It is idempotent and cannot reopen a closed owner.
func (o *Owner) Start() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errors.New("proactive_owner_closed")
	}
	if o.started {
		return nil
	}
	o.timer = o.cfg.Clock.Every(Interval, o.tick)
	o.started = true
	return nil
}
func (o *Owner) tick() {
	done, ok := o.work.Start()
	if !ok {
		return
	}
	defer done()
	scenes, err := o.cfg.Scenes()
	if err != nil {
		o.cfg.OnError(err)
		return
	}
	for _, scene := range scenes {
		if scene.Kind != "group" || !scene.ProactiveEnabled {
			continue
		}
		settled, err := o.cfg.Proactive(scene.ID, true)
		// Track settlement even when an adapter reports an error alongside a handle.
		if settled != nil {
			finish, accepted := o.work.Child()
			if accepted {
				go func() { defer finish(); <-settled }()
			}
		}
		if err != nil {
			o.cfg.OnError(err)
		}
	}
}

// Close only stops the source producer. It does NOT invent a cancellation signal
// absent from Engine's interval. Application owns source runtime cancellation.
func (o *Owner) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.closed = true
	o.work.Seal()
	if o.timer != nil {
		o.timer.Stop()
	}
}

// CloseAndWait is a Go resource-lifetime extension, not a TS behavior claim.
// Stop producers, cancel runtime at its source disposal point, then join both
// owners before closing stores. Timeout retains all ownership for a later retry.
func (o *Owner) CloseAndWait(ctx context.Context) error { o.Close(); return o.work.Wait(ctx) }

type realClock struct{}
type realInterval struct {
	stop chan struct{}
	once sync.Once
}

func (r *realInterval) Stop() { r.once.Do(func() { close(r.stop) }) }
func (realClock) Every(d time.Duration, f func()) Stopper {
	r := &realInterval{stop: make(chan struct{})}
	ticker := time.NewTicker(d)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				f()
			}
		}
	}()
	return r
}
