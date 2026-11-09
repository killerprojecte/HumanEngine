package observability

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

// LogStatusFixtureForTest is present only in this package's test variant. Its
// external HTTP tests use real Store/channel objects, including a pre-acquired
// zero-record file lease, without adding a production injection API.
type LogStatusFixtureForTest struct {
	Store    *StoreLogger
	Stdout   *logdelivery.Lease
	Registry *logdelivery.Registry
	clock    atomic.Int64
	probe    *logStatusLeaseProbe
}
type logStatusLeaseProbe struct {
	logLease
	calls atomic.Int64
	mu    sync.Mutex
	next  func()
}

func (p *logStatusLeaseProbe) Status() logdelivery.Status {
	p.calls.Add(1)
	p.mu.Lock()
	next := p.next
	p.next = nil
	p.mu.Unlock()
	if next != nil {
		next()
	}
	return p.logLease.Status()
}
func NewLogStatusFixtureForTest(directory string) (*LogStatusFixtureForTest, error) {
	registry := logdelivery.NewRegistry(io.Discard)
	stdout, err := registry.Acquire("")
	if err != nil {
		return nil, err
	}
	store := NewStoreLogger(StoreLoggerOptions{Database: ":memory:", Registry: registry, Stdout: stdout})
	f := &LogStatusFixtureForTest{Store: store, Stdout: stdout, Registry: registry}
	f.clock.Store(time.Date(2026, 11, 9, 0, 0, 0, 123000000, time.UTC).UnixMilli())
	store.now = func() time.Time { return time.UnixMilli(f.clock.Load()).UTC() }
	f.probe = &logStatusLeaseProbe{logLease: sourceLease{stdout}}
	store.stdout = f.probe
	if directory != "" {
		lease, e := registry.Acquire(directory)
		if e != nil {
			store.Close(context.Background())
			store.WaitPhysicalClose(context.Background())
			return nil, e
		}
		store.file = sourceLease{lease}
	}
	return f, nil
}
func (f *LogStatusFixtureForTest) FailNextStatus() {
	f.probe.mu.Lock()
	f.probe.next = func() { panic("synthetic_private_getter_failure") }
	f.probe.mu.Unlock()
}
func (f *LogStatusFixtureForTest) HoldNextStatus(entered chan<- struct{}, release <-chan struct{}) {
	f.probe.mu.Lock()
	f.probe.next = func() { close(entered); <-release }
	f.probe.mu.Unlock()
}
func (f *LogStatusFixtureForTest) StatusReads() int64      { return f.probe.calls.Load() }
func (f *LogStatusFixtureForTest) ResetStatusReads()       { f.probe.calls.Store(0) }
func (f *LogStatusFixtureForTest) Advance(d time.Duration) { f.clock.Add(d.Milliseconds()) }

// FailThenHoldStatus observes the failure diagnostic separately from the getter.
// Both hooks wrap a real lease; no delivery/status values are synthesized.
func (f *LogStatusFixtureForTest) FailThenHoldStatus(entered chan<- struct{}, release <-chan struct{}) {
	f.probe.mu.Lock()
	f.probe.next = func() {
		f.HoldNextStatus(entered, release)
		panic("synthetic_private_getter_failure")
	}
	f.probe.mu.Unlock()
}
