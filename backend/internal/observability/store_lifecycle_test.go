package observability

import (
	"context"
	"errors"
	"math"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

type storeTrace struct {
	mu     sync.Mutex
	events []string
}

func (s *storeTrace) add(v string) { s.mu.Lock(); s.events = append(s.events, v); s.mu.Unlock() }
func (s *storeTrace) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

type controlledLogLease struct {
	name      string
	trace     *storeTrace
	begin     func() logClose
	enqueue   func(string, bool) bool
	physical  <-chan struct{}
	syncClose func(time.Duration) *logdelivery.FlushResult
}

func (s *controlledLogLease) Enqueue(record string, priority bool) bool {
	if s.enqueue != nil {
		return s.enqueue(record, priority)
	}
	s.trace.add(s.name + "-enqueue")
	return true
}
func (s *controlledLogLease) Status() logdelivery.Status { return logdelivery.Status{State: "active"} }
func (s *controlledLogLease) beginClose() logClose {
	s.trace.add(s.name + "-begin")
	if s.begin != nil {
		return s.begin()
	}
	return settledLogClose{result: &logdelivery.FlushResult{Complete: true}}
}
func (s *controlledLogLease) CloseSync(timeout time.Duration) *logdelivery.FlushResult {
	if s.syncClose != nil {
		return s.syncClose(timeout)
	}
	return nil
}
func (s *controlledLogLease) WaitPhysicalRelease(ctx context.Context) error {
	if s.physical == nil {
		return nil
	}
	select {
	case <-s.physical:
		return nil
	default:
	}
	select {
	case <-s.physical:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type heldLogClose struct {
	name  string
	trace *storeTrace
	gate  <-chan struct{}
	err   error
}

func (s heldLogClose) Wait(context.Context) (*logdelivery.FlushResult, error) {
	<-s.gate
	s.trace.add(s.name + "-end")
	return &logdelivery.FlushResult{Complete: s.err == nil}, s.err
}
func storeSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("owner boundary not reached")
	}
}
func testStore(t *testing.T) *StoreLogger {
	t.Helper()
	l := NewStoreLogger(StoreLoggerOptions{Database: ":memory:", LookupEnv: func(string) (string, bool) { return "", false }})
	l.now = func() time.Time { return time.Date(2026, 11, 9, 0, 0, 0, 123000000, time.UTC) }
	l.newID = func() (string, error) { return "11111111-1111-4111-8111-111111111111", nil }
	return l
}

func TestStoreCloseFencesAdmissionButJoinsAlreadyAdmittedSetup(t *testing.T) {
	l := testStore(t)
	l.directory = "/synthetic/logs"
	trace := new(storeTrace)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	out := &controlledLogLease{name: "stdout", trace: trace}
	file := &controlledLogLease{name: "file", trace: trace}
	l.acquire = func(directory string) (logLease, error) {
		// Status/retention callbacks prove setup does not own the Store mutex.
		if l.Retention().Records == 0 {
			t.Error("retention did not precede acquisition")
		}
		_ = l.LoggingStatus()
		if directory == "" {
			once.Do(func() { close(entered) })
			<-release
			trace.add("acquire-stdout")
			return out, nil
		}
		trace.add("acquire-file")
		return file, nil
	}
	emitted := make(chan error, 1)
	go func() { emitted <- l.Emit(Event{Level: "info", Module: "synthetic", Code: "held"}) }()
	storeSignal(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.CloseLogs(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := l.WaitPhysicalClose(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("close lost admitted setup", err)
	}
	if err := l.Emit(Event{Level: "warn", Module: "synthetic", Code: "late"}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if l.Retention().Records != 3 {
		t.Fatal("late record and one retained-only notice missing", l.Retention())
	}
	if got := trace.snapshot(); len(got) != 0 {
		t.Fatal("close/acquire ran ahead of held admission", got)
	}
	close(release)
	if err := <-emitted; err != nil {
		t.Fatal(err)
	}
	if err := l.WaitPhysicalClose(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"acquire-stdout", "acquire-file", "stdout-enqueue", "file-enqueue", "file-begin", "stdout-begin"}
	if got := trace.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v != %v", got, want)
	}
	if err := l.Close(context.Background()); !errors.Is(err, ErrDeliveryIncomplete) {
		t.Fatal(err)
	}
	if l.LoggingStatus().ShutdownComplete == nil || *l.LoggingStatus().ShutdownComplete {
		t.Fatal("late rejection lost")
	}
}
func TestStoreCloseAllSettledBeginsFileThenStdout(t *testing.T) {
	for _, failing := range []string{"file", "stdout"} {
		t.Run(failing, func(t *testing.T) {
			l := testStore(t)
			trace := new(storeTrace)
			gate := make(chan struct{})
			bothStarted := make(chan struct{})
			done := make(chan error, 1)
			makeLease := func(name string) *controlledLogLease {
				return &controlledLogLease{name: name, trace: trace, begin: func() logClose {
					if name == "stdout" {
						close(bothStarted)
					}
					if name == failing {
						panic("private sink failure")
					}
					return heldLogClose{name: name, trace: trace, gate: gate}
				}}
			}
			l.file, l.stdout = makeLease("file"), makeLease("stdout")
			go func() { _, err := l.CloseLogs(context.Background()); done <- err }()
			storeSignal(t, bothStarted)
			select {
			case err := <-done:
				t.Fatal("one failure skipped other held sink", err)
			default:
			}
			if got := trace.snapshot(); !reflect.DeepEqual(got, []string{"file-begin", "stdout-begin"}) {
				t.Fatal(got)
			}
			close(gate)
			err := <-done
			if !errors.Is(err, errLogCloseFailed) || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
			if l.LoggingStatus().ShutdownComplete == nil || *l.LoggingStatus().ShutdownComplete {
				t.Fatal("failure not sticky")
			}
		})
	}
}
func TestStorePhysicalCloseIsIndependentOfCanceledHealthWait(t *testing.T) {
	l := testStore(t)
	trace := new(storeTrace)
	physical := make(chan struct{})
	l.stdout = &controlledLogLease{name: "stdout", trace: trace, physical: physical}
	if !errors.Is(l.WaitPhysicalClose(context.Background()), logdelivery.ErrLeaseNotReleased) {
		t.Fatal("unstarted close accepted")
	}
	if _, err := l.CloseLogs(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := l.WaitPhysicalClose(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("held physical tail reported complete", err)
	}
	close(physical)
	// A canceled error cannot be used as evidence that a physical owner still
	// exists. Prefer the observed completed boundary even under canceled context.
	if err := l.WaitPhysicalClose(ctx); err != nil {
		t.Fatal("completion lost cancellation race", err)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestStoreSyncCloseSharesDeadlineAndContainsFailures(t *testing.T) {
	l := testStore(t)
	trace := new(storeTrace)
	now := l.now()
	l.now = func() time.Time { return now }
	var deadlines []time.Duration
	l.file = &controlledLogLease{name: "file", trace: trace, syncClose: func(timeout time.Duration) *logdelivery.FlushResult {
		deadlines = append(deadlines, timeout)
		now = now.Add(3 * time.Second)
		panic("private fsync failure")
	}}
	l.stdout = &controlledLogLease{name: "stdout", trace: trace, syncClose: func(timeout time.Duration) *logdelivery.FlushResult {
		deadlines = append(deadlines, timeout)
		return &logdelivery.FlushResult{Complete: true}
	}}
	if l.CloseLogsSync(5 * time.Second) {
		t.Fatal("failed close reported complete")
	}
	if !reflect.DeepEqual(deadlines, []time.Duration{5 * time.Second, 2 * time.Second}) {
		t.Fatal(deadlines)
	}
}

type countedLogJSON struct{ calls int }

func (c *countedLogJSON) MarshalJSON() ([]byte, error) {
	c.calls++
	return []byte(`{"owned":true}`), nil
}
func TestStoreSnapshotOnceNonfiniteSurrogateAndInvalidValues(t *testing.T) {
	l := testStore(t)
	trace := new(storeTrace)
	var rows []string
	out := &controlledLogLease{name: "stdout", trace: trace, enqueue: func(record string, _ bool) bool { rows = append(rows, record); return true }}
	file := &controlledLogLease{name: "file", trace: trace, enqueue: func(record string, _ bool) bool { rows = append(rows, record); return true }}
	l.stdout, l.file = out, file
	called := new(countedLogJSON)
	fields := map[string]any{"custom": called, "nan": math.NaN(), "surrogate": jsjson.FromUTF16([]uint16{0xd800})}
	if err := l.Emit(Event{Level: "info", Module: "synthetic", Code: "snapshot", Fields: fields}); err != nil {
		t.Fatal(err)
	}
	if called.calls != 1 || len(rows) != 2 || !strings.HasSuffix(rows[0], " | "+rows[1]) {
		t.Fatal(called.calls, rows)
	}
	if !strings.Contains(rows[1], `"nan":null`) || !strings.Contains(rows[1], `"surrogate":"\ud800"`) {
		t.Fatal(rows[1])
	}
	fields["custom"] = "mutated"
	if strings.Contains(string(l.RetainedJSON()), "mutated") {
		t.Fatal("retained caller alias")
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, bad := range []any{big.NewInt(1), cycle, make(chan int)} {
		if err := l.Emit(Event{Level: "warn", Fields: map[string]any{"bad": bad}}); !errors.Is(err, ErrUnserializableLog) {
			t.Fatal(err)
		}
	}
	if len(rows) != 2 || l.LoggingStatus().UnserializableRecords != 3 {
		t.Fatal(rows, l.LoggingStatus())
	}
}

func TestStoreSyncDeadlineIncludesHeldAdmissionAndRetainsTail(t *testing.T) {
	l := testStore(t)
	l.directory = "/synthetic/logs"
	trace := new(storeTrace)
	entered, release, emitted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l.acquire = func(directory string) (logLease, error) {
		name := "file"
		if directory == "" {
			name = "stdout"
			close(entered)
			<-release
		}
		return &controlledLogLease{name: name, trace: trace}, nil
	}
	go func() {
		defer close(emitted)
		if err := l.Emit(Event{Level: "info", Module: "synthetic", Code: "held"}); err != nil {
			t.Error(err)
		}
	}()
	storeSignal(t, entered)
	returned := make(chan bool, 1)
	go func() { returned <- l.CloseLogsSync(time.Millisecond) }()
	select {
	case complete := <-returned:
		if complete {
			t.Fatal("held admission reported complete")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sync deadline did not bound admission")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.WaitPhysicalClose(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("tail released before acquisition returned", err)
	}
	if got := trace.snapshot(); len(got) != 0 {
		t.Fatal("began close before acquire returned", got)
	}
	close(release)
	storeSignal(t, emitted)
	if err := l.WaitPhysicalClose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(context.Background()); !errors.Is(err, ErrDeliveryIncomplete) {
		t.Fatal("timeout health must remain sticky", err)
	}
	want := []string{"stdout-enqueue", "file-enqueue", "file-begin", "stdout-begin", "file-begin", "stdout-begin"}
	if got := trace.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}
