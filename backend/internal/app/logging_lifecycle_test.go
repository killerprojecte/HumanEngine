package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

type applicationHeldLogWriter struct {
	gate, entered     chan struct{}
	once, releaseOnce sync.Once
	mu                sync.Mutex
	data              bytes.Buffer
}

func (w *applicationHeldLogWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.gate
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.Write(p)
}
func (w *applicationHeldLogWriter) release() { w.releaseOnce.Do(func() { close(w.gate) }) }
func (w *applicationHeldLogWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}
func loggingSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatal("logging ownership boundary not reached")
	}
}
func assertApplicationWriterHeld(t *testing.T, o Options) {
	t.Helper()
	other, err := installation.Open(context.Background(), o.Installation)
	if other != nil {
		other.Close()
		t.Fatal("installation admitted overlapping writer")
	}
	if !errors.Is(err, installation.ErrWriterBusy) {
		t.Fatal("wrong writer-lock result", err)
	}
}
func assertApplicationWriterReleased(t *testing.T, o Options) {
	t.Helper()
	other, err := installation.Open(context.Background(), o.Installation)
	if err != nil {
		t.Fatal("installation not reusable after physical release", err)
	}
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
}
func sourceApplicationLogger(t *testing.T, a *Application) *observability.StoreLogger {
	t.Helper()
	l, ok := a.Logger().(*observability.StoreLogger)
	if !ok {
		t.Fatalf("default must use StoreLogger, got %T", a.Logger())
	}
	return l
}

func TestApplicationDefaultStoreLoggerOwnsFileAndStdout(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	if options.Logs != nil {
		t.Fatal("fixture bypasses the default source owner")
	}
	a, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(context.Background())
	logs := sourceApplicationLogger(t, a)
	if !a.options.ownsLogs {
		t.Fatal("default not owned")
	}
	if err = logs.Emit(observability.Event{Level: "warn", Module: "test", Code: "source-probe", TraceID: "trace", TurnID: "turn", Fields: map[string]any{"role": "thinker"}}); err != nil {
		t.Fatal(err)
	}
	if err = a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !logs.LoggingStatus().Closed || logs.LoggingStatus().ShutdownComplete == nil || !*logs.LoggingStatus().ShutdownComplete {
		t.Fatal(logs.LoggingStatus())
	}
	if err = logs.WaitPhysicalClose(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(options.Installation.Database), "logs/runtime.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	output := options.Diagnostics.(*applicationLogBuffer).snapshot()
	human := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != len(human) || len(lines) == 0 {
		t.Fatal("source dual-sink record mismatch", len(lines), len(human))
	}
	for i, line := range lines {
		if !strings.HasSuffix(human[i], " | "+line) {
			t.Fatal("stdout is not readable prefix plus identical flat row", human[i], line)
		}
	}
	if strings.Count(string(data), `"code":"app.drained"`) != 1 || !strings.Contains(string(data), `"role":"replyer"`) {
		t.Fatal(string(data))
	}
	previous := a.loggingClose
	if err = a.ShutdownAndFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.loggingClose != previous {
		t.Fatal("repeated close replaced its operation")
	}
	assertApplicationWriterReleased(t, options)
}
func TestApplicationBorrowedShutdownLeavesRawLoggerOpen(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	raw := observability.New(io.Discard, io.Discard)
	defer raw.Close(context.Background())
	options.Logs = raw
	a, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer a.ShutdownAndFlush(context.Background())
	if err = a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if raw.Status().Closed || a.loggingClose != nil {
		t.Fatal("borrowed Shutdown closed foreign lifetime")
	}
	if err = raw.Emit(observability.Event{Level: "info", Module: "test", Code: "borrowed-still-open"}); err != nil {
		t.Fatal(err)
	}
	assertApplicationWriterReleased(t, options)
	if err = a.ShutdownAndFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !raw.Status().Closed {
		t.Fatal("explicit finalization did not close borrowed logger")
	}
}
func TestApplicationLoggingTailRetainsWriterAndRetriesSameOperation(t *testing.T) {
	for _, borrowed := range []bool{false, true} {
		name := "default-source"
		if borrowed {
			name = "explicit-raw"
		}
		t.Run(name, func(t *testing.T) {
			options := applicationOptions(t)
			options.Entries = []string{}
			writer := &applicationHeldLogWriter{gate: make(chan struct{}), entered: make(chan struct{})}
			if borrowed {
				options.Logs = observability.New(writer, io.Discard)
			} else {
				options.LogRegistry = logdelivery.NewRegistry(writer)
			}
			a, err := Open(context.Background(), options)
			if err != nil {
				writer.release()
				t.Fatal(err)
			}
			defer a.ShutdownAndFlush(context.Background())
			defer writer.release()
			loggingSignal(t, writer.entered)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = a.ShutdownAndFlush(ctx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) || a.finalized || !a.producersDrained || a.loggingClose == nil {
				t.Fatal("did not retain physical logging tail", err, a.finalized, a.producersDrained)
			}
			operation := a.loggingClose
			assertApplicationWriterHeld(t, options)
			canceled, cancelWait := context.WithCancel(context.Background())
			cancelWait()
			if err = a.ShutdownAndFlush(canceled); !errors.Is(err, context.Canceled) || a.loggingClose != operation {
				t.Fatal("retry replaced close or ignored cancellation", err)
			}
			writer.release()
			loggingSignal(t, operation.done)
			if err = a.ShutdownAndFlush(canceled); err != nil {
				t.Fatal("completed physical operation lost canceled waiter", err)
			}
			if a.loggingClose != operation || !a.finalized {
				t.Fatal("final operation identity changed")
			}
			if strings.Count(writer.text(), `"code":"app.drained"`) != 1 {
				t.Fatal("final log duplicated", writer.text())
			}
			assertApplicationWriterReleased(t, options)
		})
	}
}
func TestApplicationSourceHealthFailureStillReleasesDatabase(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	bad := filepath.Join(filepath.Dir(options.Installation.Database), "logs/runtime.jsonl")
	if err := os.MkdirAll(bad, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(context.Background())
	logs := sourceApplicationLogger(t, a)
	err = a.Shutdown(context.Background())
	if !errors.Is(err, observability.ErrDeliveryIncomplete) || !a.finalized || !a.logsFinalized {
		t.Fatal("health failure skipped completed physical/database cleanup", err)
	}
	if err = logs.WaitPhysicalClose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if logs.LoggingStatus().File == nil || logs.LoggingStatus().File.LastFailure == nil {
		t.Fatal("real file-open failure not visible")
	}
	assertApplicationWriterReleased(t, options)
	if a.loggingClose == nil {
		t.Fatal("source close operation absent")
	}
	operation := a.loggingClose
	if err = a.Shutdown(context.Background()); !errors.Is(err, observability.ErrDeliveryIncomplete) || a.loggingClose != operation {
		t.Fatal("terminal health result not retained", err)
	}
}
func TestApplicationStartupHealthFailureReturnsNoFinalizedPartial(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	bad := filepath.Join(filepath.Dir(options.Installation.Database), "logs/runtime.jsonl")
	if err := os.MkdirAll(bad, 0700); err != nil {
		t.Fatal(err)
	}
	original := errors.New("synthetic_relation_startup_failure")
	var captured *Application
	options.relationExposurePort = func(a *Application, _ Owners) (*memoryservice.ExposurePort, error) {
		captured = a
		_ = a.Logger().Emit(observability.Event{Level: "error", Module: "test", Code: "startup-probe"})
		return nil, original
	}
	partial, err := Open(context.Background(), options)
	if !errors.Is(err, original) || !errors.Is(err, observability.ErrDeliveryIncomplete) || partial != nil || captured == nil || !captured.finalized {
		t.Fatal("startup error/cleanup contract", err, partial, captured)
	}
	assertApplicationWriterReleased(t, options)
}
func TestApplicationNonlastStoreDoesNotJoinSiblingLifetime(t *testing.T) {
	optionsA, optionsB := applicationOptions(t), applicationOptions(t)
	optionsA.Entries = []string{}
	optionsB.Entries = []string{}
	registry := logdelivery.NewRegistry(io.Discard)
	optionsA.LogRegistry, optionsB.LogRegistry = registry, registry
	a, err := Open(context.Background(), optionsA)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(context.Background())
	b, err := Open(context.Background(), optionsB)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = a.Shutdown(ctx); err != nil {
		t.Fatal("nonlast Store waited sibling lifetime", err)
	}
	if sourceApplicationLogger(t, b).LoggingStatus().Closed {
		t.Fatal("sibling Store was closed")
	}
	if err = b.Logger().Emit(observability.Event{Level: "info", Module: "test", Code: "sibling-still-running"}); err != nil {
		t.Fatal(err)
	}
	assertApplicationWriterReleased(t, optionsA)
	if err = b.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
