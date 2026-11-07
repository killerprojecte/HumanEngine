package workingset

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Timer delivery is explicit and can occur while another callback is blocked.
// There are no sleeps or real-time scheduling assumptions in these tests.
type concurrentScheduler struct {
	mu    sync.Mutex
	tasks []*concurrentTimer
}
type concurrentTimer struct {
	fn       func()
	delay    time.Duration
	canceled atomic.Bool
}

func (t *concurrentTimer) Cancel() { t.canceled.Store(true) }
func (s *concurrentScheduler) Schedule(delay time.Duration, fn func()) CancelTimer {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := &concurrentTimer{fn: fn, delay: delay}
	s.tasks = append(s.tasks, task)
	return task
}
func (s *concurrentScheduler) fire() bool {
	for {
		s.mu.Lock()
		if len(s.tasks) == 0 {
			s.mu.Unlock()
			return false
		}
		task := s.tasks[0]
		s.tasks = s.tasks[1:]
		s.mu.Unlock()
		if !task.canceled.Load() {
			task.fn()
			return true
		}
	}
}
func (s *concurrentScheduler) pending() (int, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	var delay time.Duration
	for _, task := range s.tasks {
		if !task.canceled.Load() {
			n++
			delay = task.delay
		}
	}
	return n, delay
}
func TestMaintenanceConcurrentNotificationsNeverOverlapDrains(t *testing.T) {
	scheduler := &concurrentScheduler{}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var calls, active, peak atomic.Int32
	m, e := NewMaintenance(MaintenancePorts{Enabled: func() (bool, error) { return true, nil }, DrainOneBatch: func() (bool, error) {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		defer active.Add(-1)
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return false, nil
	}}, 250, scheduler)
	must(t, e)
	m.Start()
	go func() { scheduler.fire(); close(done) }()
	<-entered
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Notify() }()
	}
	wg.Wait()
	if n, _ := scheduler.pending(); n != 1 {
		t.Fatalf("notifications did not coalesce: %d", n)
	}
	if !scheduler.fire() {
		t.Fatal("missing timer")
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent drain")
	}
	for i := 0; i < 20; i++ {
		m.Notify()
	}
	if n, _ := scheduler.pending(); n != 0 {
		t.Fatal("deferred tick duplicated", n)
	}
	close(release)
	<-done
	if n, d := scheduler.pending(); n != 1 || d != 0 {
		t.Fatal("expired notification not resumed exactly once", n, d)
	}
	scheduler.fire()
	if calls.Load() != 2 || peak.Load() != 1 {
		t.Fatal(calls.Load(), peak.Load())
	}
	if n, _ := scheduler.pending(); n != 0 {
		t.Fatal("unexpected maintenance loop", n)
	}
}
func TestMaintenanceInFlightLifecycleChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		more   bool
		action func(*Maintenance)
		want   int
	}{
		{"cancel stops pending notification", false, func(m *Maintenance) { m.Cancel() }, 0},
		{"cancel retains running more continuation", true, func(m *Maintenance) { m.Cancel() }, 1},
		{"configure keeps one new notification", false, func(m *Maintenance) { m.Configure() }, 1},
		{"dispose revokes running continuation", true, func(m *Maintenance) { m.Dispose() }, 0},
		{"dispose start queues new lifecycle", true, func(m *Maintenance) { m.Dispose(); m.Start() }, 1},
		{"new lifecycle cancel cannot revive old more", true, func(m *Maintenance) { m.Dispose(); m.Start(); m.Cancel() }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheduler := &concurrentScheduler{}
			entered := make(chan struct{})
			release := make(chan struct{})
			done := make(chan struct{})
			var calls, active, peak atomic.Int32
			m, e := NewMaintenance(MaintenancePorts{Enabled: func() (bool, error) { return true, nil }, DrainOneBatch: func() (bool, error) {
				n := active.Add(1)
				if n > peak.Load() {
					peak.Store(n)
				}
				defer active.Add(-1)
				if calls.Add(1) == 1 {
					close(entered)
					<-release
					return tc.more, nil
				}
				return false, nil
			}}, 250, scheduler)
			must(t, e)
			m.Start()
			go func() { scheduler.fire(); close(done) }()
			<-entered
			m.Notify()
			scheduler.fire()
			tc.action(m)
			// Any new generation timer delivered before the old callback exits must
			// become deferred work rather than overlapping that callback.
			scheduler.fire()
			if calls.Load() != 1 {
				t.Fatal("new lifecycle overlapped old drain")
			}
			close(release)
			<-done
			if n, _ := scheduler.pending(); n != tc.want {
				t.Fatal("pending", n, "want", tc.want)
			}
			if tc.want > 0 {
				scheduler.fire()
			}
			if peak.Load() != 1 {
				t.Fatal("overlap", peak.Load())
			}
			if n, _ := scheduler.pending(); n != 0 {
				t.Fatal("loop", n)
			}
		})
	}
}
func TestMaintenanceSynchronousNotifyConfigureCallOrder(t *testing.T) {
	scheduler := &concurrentScheduler{}
	trace := []string{}
	var m *Maintenance
	drains := 0
	m, e := NewMaintenance(MaintenancePorts{Enabled: func() (bool, error) { trace = append(trace, "enabled"); return true, nil }, DrainOneBatch: func() (bool, error) {
		drains++
		if drains == 1 {
			trace = append(trace, "drain begin")
			m.Notify()
			m.Configure()
			trace = append(trace, "drain end")
			return true, nil
		}
		return false, nil
	}}, 250, scheduler)
	must(t, e)
	m.Start()
	scheduler.fire()
	want := []string{"enabled", "enabled", "drain begin", "enabled", "enabled", "drain end"}
	if !reflect.DeepEqual(trace, want) {
		t.Fatal(trace, want)
	}
	if n, d := scheduler.pending(); n != 1 || d != 250*time.Millisecond {
		t.Fatal(n, d)
	}
	scheduler.fire()
	if drains != 2 {
		t.Fatal(drains)
	}
	if n, _ := scheduler.pending(); n != 0 {
		t.Fatal(n)
	}
}
