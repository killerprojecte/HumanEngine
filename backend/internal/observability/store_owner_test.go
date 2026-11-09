package observability

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

type storeOwnerWriter struct {
	mu      sync.Mutex
	data    bytes.Buffer
	entered chan struct{}
	gate    <-chan struct{}
	once    sync.Once
}

func (w *storeOwnerWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		if w.entered != nil {
			close(w.entered)
		}
	})
	if w.gate != nil {
		<-w.gate
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.Write(p)
}
func (w *storeOwnerWriter) text() string { w.mu.Lock(); defer w.mu.Unlock(); return w.data.String() }
func ownedStore(t *testing.T, database string, registry *logdelivery.Registry) *StoreLogger {
	t.Helper()
	return NewStoreLogger(StoreLoggerOptions{Database: database, Registry: registry, LookupEnv: func(string) (string, bool) { return "", false }})
}

func TestStorePhysicalOwnersAreIndependentAndLastLeaseOnly(t *testing.T) {
	directory := t.TempDir()
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	writer := &storeOwnerWriter{gate: gate, entered: make(chan struct{})}
	registry := logdelivery.NewRegistry(writer)
	a := ownedStore(t, filepath.Join(directory, "a.sqlite"), registry)
	b := ownedStore(t, filepath.Join(directory, "b.sqlite"), registry)
	if a.LoggingStatus().File != nil || a.LoggingStatus().Stdout != nil {
		t.Fatal("constructor/status acquired leases")
	}
	if _, err := os.Stat(filepath.Join(directory, "logs")); !os.IsNotExist(err) {
		t.Fatal("lazy status created directory", err)
	}
	if err := a.Emit(Event{Level: "info", Module: "synthetic", Code: "one"}); err != nil {
		t.Fatal(err)
	}
	storeSignal(t, writer.entered)
	if err := b.Emit(Event{Level: "warn", Module: "synthetic", Code: "two"}); err != nil {
		t.Fatal(err)
	}
	// stdout physical Write is held. The independent real file owner must still
	// flush and fsync through its own lease while stdout retains whole credits.
	a.mu.Lock()
	file := a.file.(sourceLease)
	a.mu.Unlock()
	result, err := file.Close(context.Background())
	if err != nil || !result.Complete || !result.Durable || result.Status.WrittenRecords != 2 {
		t.Fatal(result, err)
	}
	fileBytes, err := os.ReadFile(filepath.Join(directory, "logs", "runtime.jsonl"))
	if err != nil || bytes.Count(fileBytes, []byte{'\n'}) != 2 {
		t.Fatal(string(fileBytes), err)
	}
	if a.LoggingStatus().Stdout.WrittenRecords != 0 || a.LoggingStatus().Stdout.PendingRecords != 2 {
		t.Fatal(a.LoggingStatus())
	}
	release()
	// A's file lease was already released nonlast. A must not wait on B's shared
	// file/stdout workers even though those native generations remain alive.
	if err = a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = a.WaitPhysicalClose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.LoggingStatus().Closed {
		t.Fatal("one Store closed sibling")
	}
	if err = b.Emit(Event{Level: "info", Module: "synthetic", Code: "three"}); err != nil {
		t.Fatal(err)
	}
	if err = b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = b.WaitPhysicalClose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.LoggingStatus().Stdout.AcceptedRecords != 3 {
		t.Fatal("retired nonlast lease stopped observing own generation", a.LoggingStatus())
	}
	// Both source sinks contain the same three flat rows, and stdout contains a
	// readable prefix rather than a second native stderr stream.
	fileBytes, err = os.ReadFile(filepath.Join(directory, "logs", "runtime.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	plain := strings.Split(strings.TrimSuffix(string(fileBytes), "\n"), "\n")
	human := strings.Split(strings.TrimSuffix(writer.text(), "\n"), "\n")
	if len(plain) != 3 || len(human) != 3 {
		t.Fatal(plain, human)
	}
	for i := range plain {
		if !strings.HasSuffix(human[i], " | "+plain[i]) {
			t.Fatal("double serialization or wrong stdout topology", human[i], plain[i])
		}
	}
}
func TestStoreCanceledCloseRetainsRealNativeWriterUntilDone(t *testing.T) {
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	writer := &storeOwnerWriter{gate: gate, entered: make(chan struct{})}
	registry := logdelivery.NewRegistry(writer)
	l := ownedStore(t, ":memory:", registry)
	if err := l.Emit(Event{Level: "info", Module: "synthetic", Code: "held"}); err != nil {
		t.Fatal(err)
	}
	storeSignal(t, writer.entered)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := l.WaitPhysicalClose(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("held native owner disappeared", err)
	}
	release()
	joined, cancelJoin := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelJoin()
	if err := l.WaitPhysicalClose(joined); err != nil {
		t.Fatal(err)
	}
	// The canceled consumer can now observe physical completion without retrying
	// generation acquisition or assuming the old cancellation still means alive.
	if err := l.WaitPhysicalClose(ctx); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writer.text(), `"code":"held"`) {
		t.Fatal(writer.text())
	}
}
