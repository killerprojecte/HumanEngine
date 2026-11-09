package workingset

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sync"
	"time"
)

// TimerScheduler permits deterministic event-loop tests. Schedule must return
// before invoking its callback. Cancel suppresses callbacks not already begun.
type TimerScheduler interface {
	Schedule(time.Duration, func()) CancelTimer
}
type CancelTimer interface{ Cancel() }
type systemScheduler struct{}
type systemTimer struct{ timer *time.Timer }

func (systemScheduler) Schedule(d time.Duration, fn func()) CancelTimer {
	return systemTimer{time.AfterFunc(d, fn)}
}
func (t systemTimer) Cancel() { t.timer.Stop() }

type MaintenancePorts struct {
	Enabled func() (bool, error)
	// EnabledInSession borrows only this synchronous notification's capability.
	EnabledInSession func(*recovery.WriteSession) (bool, error)
	DrainOneBatch    func() (bool, error)
	Failed           func()
}

// Maintenance has one notification-driven timer, no periodic scan or startup
// source replay. Dispose revokes a running callback's ability to reschedule.
type Maintenance struct {
	mu         sync.Mutex
	work       quiescence.Group
	ports      MaintenancePorts
	interval   time.Duration
	scheduler  TimerScheduler
	started    bool
	closed     bool
	timer      CancelTimer
	generation uint64
	scheduled  bool
	inFlight   bool
	deferred   bool
	lifecycle  uint64
}

func NewMaintenance(ports MaintenancePorts, intervalMs float64, scheduler TimerScheduler) (*Maintenance, error) {
	if !safe(intervalMs) || intervalMs < 10 {
		return nil, errors.New("working_set_maintenance_interval")
	}
	if ports.Enabled == nil || ports.DrainOneBatch == nil {
		return nil, errors.New("working_set_maintenance_dependencies_required")
	}
	if scheduler == nil {
		scheduler = systemScheduler{}
	}
	// Node setTimeout normalizes overflowing signed-32-bit delays to 1ms.
	delay := intervalMs
	if delay > 2147483647 {
		delay = 1
	}
	return &Maintenance{ports: ports, interval: time.Duration(delay) * time.Millisecond, scheduler: scheduler}, nil
}
func (m *Maintenance) failed() {
	if m.ports.Failed != nil {
		func() { defer func() { _ = recover() }(); m.ports.Failed() }()
	}
}
func (m *Maintenance) Start() {
	done, ok := m.work.Start()
	if !ok {
		return
	}
	defer done()
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	m.Notify()
}
func (m *Maintenance) Notify() { m.notify(nil, nil) }

// NotifyInSession never retains the capability in the scheduled timer.
func (m *Maintenance) NotifyInSession(session *recovery.WriteSession) { m.notify(nil, session) }
func (m *Maintenance) notify(lifecycle *uint64, session *recovery.WriteSession) {
	done, ok := m.work.Start()
	if !ok {
		return
	}
	defer done()
	m.mu.Lock()
	if !m.started || m.scheduled || (lifecycle != nil && *lifecycle != m.lifecycle) {
		m.mu.Unlock()
		return
	}
	generation := m.generation
	m.mu.Unlock()
	var yes bool
	var e error
	if session != nil && m.ports.EnabledInSession != nil {
		yes, e = m.ports.EnabledInSession(session)
	} else {
		yes, e = m.ports.Enabled()
	}
	if e != nil {
		m.failed()
		return
	}
	if !yes {
		return
	}
	m.mu.Lock()
	if !m.started || m.scheduled || m.generation != generation {
		m.mu.Unlock()
		return
	}
	m.scheduled = true
	m.timer = m.scheduler.Schedule(m.interval, func() { m.tick(generation) })
	m.mu.Unlock()
}

// tick serializes actual work, not just timer creation. Notify keeps the
// source's synchronous Enabled timing even when called from DrainOneBatch.
// A timer that expires during that callback becomes one deferred event-loop
// tick; it cannot run a second drain or lose a later notification.
func (m *Maintenance) tick(generation uint64) {
	done, ok := m.work.Start()
	if !ok {
		return
	}
	defer done()
	m.mu.Lock()
	if !m.started || !m.scheduled || m.generation != generation {
		m.mu.Unlock()
		return
	}
	m.timer = nil
	if m.inFlight {
		m.deferred = true
		m.mu.Unlock()
		return
	}
	m.scheduled = false
	m.inFlight = true
	lifecycle := m.lifecycle
	m.mu.Unlock()
	more := false
	yes, e := m.ports.Enabled()
	if e == nil && yes {
		more, e = m.ports.DrainOneBatch()
	}
	if e != nil {
		m.failed()
		more = false
	}
	m.mu.Lock()
	m.inFlight = false
	// Keep the pending notification's generation, which may belong to a new
	// Configure/Start. An old drain never overlaps that newer lifecycle's work.
	if m.started && m.scheduled && m.deferred {
		m.deferred = false
		next := m.generation
		m.timer = m.scheduler.Schedule(0, func() { m.tick(next) })
	}
	continueDrain := more && m.started && lifecycle == m.lifecycle
	m.mu.Unlock()
	if continueDrain {
		m.notify(&lifecycle, nil)
	}
}

// Cancel drops pending timer work, not the started flag or running callback.
// As in the source, an already running successful drain returning true may
// notify again. Dispose additionally revokes that lifecycle continuation.
func (m *Maintenance) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	if m.timer != nil {
		m.timer.Cancel()
	}
	m.timer = nil
	m.scheduled = false
	m.deferred = false
}
func (m *Maintenance) Configure() { m.Cancel(); m.Notify() }
func (m *Maintenance) Dispose() {
	m.mu.Lock()
	m.started = false
	m.lifecycle++
	m.generation++
	if m.timer != nil {
		m.timer.Cancel()
	}
	m.timer = nil
	m.scheduled = false
	m.deferred = false
	m.mu.Unlock()
}

// CloseAndWait is permanent application shutdown. Dispose keeps its existing
// restartable source semantics; this boundary additionally seals all root owner
// reads and waits actual Enabled/drain/failure callbacks, not just timer Stop.
func (m *Maintenance) CloseAndWait(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.work.Seal()
	m.mu.Unlock()
	m.Dispose()
	return m.work.Wait(ctx)
}
