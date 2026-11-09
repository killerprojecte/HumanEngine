package observability

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

type heldStoreWriter struct {
	gate chan struct{}
	once sync.Once
}

func (w *heldStoreWriter) Write(p []byte) (int, error) { <-w.gate; return len(p), nil }
func (w *heldStoreWriter) release()                    { w.once.Do(func() { close(w.gate) }) }

type standaloneStoreLease struct{ *logdelivery.Channel }
type standaloneStoreClose struct {
	done   chan struct{}
	result logdelivery.FlushResult
}

func (o *standaloneStoreClose) Wait(context.Context) (*logdelivery.FlushResult, error) {
	<-o.done
	return &o.result, nil
}
func (s standaloneStoreLease) beginClose() logClose {
	o := &standaloneStoreClose{done: make(chan struct{})}
	go func() { o.result, _ = s.Flush(context.Background(), logdelivery.DefaultTimeout, true); close(o.done) }()
	return o
}
func (s standaloneStoreLease) CloseSync(timeout time.Duration) *logdelivery.FlushResult {
	result, _ := s.Flush(context.Background(), timeout, true)
	return &result
}
func (s standaloneStoreLease) WaitPhysicalRelease(ctx context.Context) error {
	select {
	case <-s.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type failingStoreLease struct{ status logdelivery.Status }

func (s failingStoreLease) Status() logdelivery.Status                       { return s.status }
func (s failingStoreLease) Enqueue(string, bool) bool                        { panic("private file enqueue failure") }
func (s failingStoreLease) beginClose() logClose                             { return settledLogClose{} }
func (s failingStoreLease) CloseSync(time.Duration) *logdelivery.FlushResult { return nil }
func (s failingStoreLease) WaitPhysicalRelease(context.Context) error        { return nil }
func legacyField(t *testing.T, fixture map[string]json.RawMessage, key string, value any) {
	t.Helper()
	raw, ok := fixture[key]
	if !ok {
		t.Fatal("missing source field", key)
	}
	var want any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	equalStoreJSON(t, value, want)
}

// These seven original actual-source interaction objects are unchanged. Unlike
// the boundary-only 26-case fixture, these cases run the native bounded channel
// with a held physical writer so source pending-credit snapshots are exact.
func TestSourceStoreChannelInteractions(t *testing.T) {
	bytes, err := os.ReadFile("log-store-channel-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Interactions          []map[string]json.RawMessage
		OriginalFixtureSHA256 string
	}
	if err = json.Unmarshal(bytes, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Interactions) != 7 || fixture.OriginalFixtureSHA256 != "fc659f1726b856ffad01d66f8d9c2fb43150d5a4bfb4b406749379f067059dd5" {
		t.Fatal("original source evidence changed")
	}
	for _, c := range fixture.Interactions {
		var name string
		if err = json.Unmarshal(c["name"], &name); err != nil {
			t.Fatal(err)
		}
		t.Run(name, func(t *testing.T) {
			l := testStore(t)
			if name == "unused-store-is-lazy-and-clean-close-is-noop" {
				legacyField(t, c, "before", l.LoggingStatus())
				result, err := l.CloseLogs(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				legacyField(t, c, "result", result)
				legacyField(t, c, "after", l.LoggingStatus())
				return
			}
			if name == "close-allsettled-drains-other-sink-before-error" {
				replayLegacyStoreSettled(t, c)
				return
			}
			writer := &heldStoreWriter{gate: make(chan struct{})}
			options := logdelivery.Options{}
			if name == "priority-original-info" || name == "priority-original-warn" {
				bytes, records := int64(1024), int64(8)
				options.MaxBytes, options.MaxRecords = &bytes, &records
			}
			channel, err := logdelivery.NewChannel(options, writer)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				writer.release()
				_, _ = channel.Flush(context.Background(), time.Second, true)
				select {
				case <-channel.Done():
				case <-time.After(3 * time.Second):
					t.Error("native channel physical owner remains")
				}
			})
			l.stdout = standaloneStoreLease{channel}
			event := Event{Level: "info", Module: "synthetic", Code: "ordinary"}
			switch name {
			case "priority-original-info", "priority-original-warn":
				for i := 0; i < 7; i++ {
					if !channel.Enqueue("x", false) {
						t.Fatal("source prefix rejected")
					}
				}
				event.Code = "priority"
				override := "error"
				if name == "priority-original-warn" {
					event.Level = "warn"
					override = "info"
				}
				event.Fields = map[string]any{"level": override}
				if err = l.Emit(event); err != nil {
					t.Fatal(err)
				}
			case "unserializable-is-not-admitted":
				event.Level = "warn"
				event.Code = "non-json"
				event.Fields = map[string]any{"counts": map[string]any{"synthetic": big.NewInt(1)}}
				if err = l.Emit(event); !errors.Is(err, ErrUnserializableLog) {
					t.Fatal(err)
				}
			case "initialization-failure-does-not-undo-stdout":
				l.file = failingStoreLease{status: channel.Status()}
				event.Code = "second-channel-throws"
				if err = l.Emit(event); err != nil {
					t.Fatal(err)
				}
			case "late-log-retained-and-rechecked-shutdown":
				if err = l.Emit(event); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal(name)
			}
			if name != "late-log-retained-and-rechecked-shutdown" {
				legacyField(t, c, "before", l.LoggingStatus())
			}
			writer.release()
			result, err := l.CloseLogs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if name == "late-log-retained-and-rechecked-shutdown" {
				legacyField(t, c, "firstClose", result)
				event.Level = "warn"
				event.Code = "after-close"
				if err = l.Emit(event); !errors.Is(err, ErrClosed) {
					t.Fatal(err)
				}
				legacyField(t, c, "afterLateLog", l.LoggingStatus())
				result, err = l.CloseLogs(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				legacyField(t, c, "secondClose", result)
			} else {
				legacyField(t, c, "result", result)
			}
			legacyField(t, c, "after", l.LoggingStatus())
			if err = l.WaitPhysicalClose(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func replayLegacyStoreSettled(t *testing.T, c map[string]json.RawMessage) {
	l := testStore(t)
	trace := new(storeTrace)
	gate, begun := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	l.file = &legacyHeldStoreLease{trace: trace, gate: gate}
	l.stdout = &controlledLogLease{name: "unused", trace: new(storeTrace), begin: func() logClose {
		trace.add("stdout-reject")
		close(begun)
		return settledLogClose{err: io.ErrClosedPipe}
	}}
	go func() { _, err := l.CloseLogs(context.Background()); trace.add("store-reject"); done <- err }()
	storeSignal(t, begun)
	select {
	case <-done:
		t.Fatal("did not await held file")
	default:
	}
	close(gate)
	if err := <-done; err == nil {
		t.Fatal("missing failure")
	}
	var want []string
	if err := json.Unmarshal(c["events"], &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trace.snapshot(), want) {
		t.Fatal(trace.snapshot(), want)
	}
	legacyField(t, c, "shutdownComplete", l.LoggingStatus().ShutdownComplete)
}

type legacyHeldStoreLease struct {
	trace *storeTrace
	gate  <-chan struct{}
}

func (s *legacyHeldStoreLease) Status() logdelivery.Status { return logdelivery.Status{State: "ready"} }
func (s *legacyHeldStoreLease) Enqueue(string, bool) bool  { return true }
func (s *legacyHeldStoreLease) beginClose() logClose {
	s.trace.add("file-start")
	return heldLogClose{name: "file", trace: s.trace, gate: s.gate}
}
func (s *legacyHeldStoreLease) CloseSync(time.Duration) *logdelivery.FlushResult { return nil }
func (s *legacyHeldStoreLease) WaitPhysicalRelease(context.Context) error        { return nil }
