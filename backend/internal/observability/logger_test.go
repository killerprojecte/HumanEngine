package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type slowSink struct {
	entered chan struct{}
	release chan struct{}
	bytes.Buffer
}

func (s *slowSink) Write(b []byte) (int, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return s.Buffer.Write(b)
}
func TestSlowSinkDoesNotBlockProducerAndShutdownDrains(t *testing.T) {
	sink := &slowSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	l := New(sink, nil)
	if err := l.Emit(Event{Level: "info", Code: "first", Message: "诊断内容"}); err != nil {
		t.Fatal(err)
	}
	<-sink.entered
	for i := 0; i < 1000; i++ {
		if err := l.Emit(Event{Code: "queued", Fields: map[string]any{"index": i}}); err != nil {
			t.Fatal(err)
		}
	}
	if l.Status().Queued != 1000 {
		t.Fatal("events dropped")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(sink.release)
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.Status().Written != 1001 || strings.Count(sink.String(), "\n") != 1001 {
		t.Fatal("incomplete drain")
	}
	if err := l.Emit(Event{}); err != ErrClosed {
		t.Fatal("accepted after close")
	}
}

type failedSink struct{}

func (failedSink) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestFailuresVisibleAndMessagesRemainUseful(t *testing.T) {
	l := New(failedSink{}, nil)
	_ = l.Emit(Event{Code: "failure"})
	if l.Close(context.Background()) == nil || l.Status().Failed != 1 {
		t.Fatal("write error hidden")
	}
	e := Event{At: time.Unix(0, 0), Level: "error", Module: "model", Code: "call.failed", Message: "provider returned 429", TraceID: "trace-1", DurationMS: 12, Fields: map[string]any{"status": 429, "retryAfterSeconds": 5}}
	rendered := Render(e)
	for _, want := range []string{"provider returned 429", "trace=trace-1", "12.000ms", `"status":429`} {
		if !strings.Contains(rendered, want) {
			t.Fatal(rendered)
		}
	}
}

type changingMarshaler struct{ calls int }

func (m *changingMarshaler) MarshalJSON() ([]byte, error) {
	m.calls++
	return []byte(fmt.Sprintf(`{"snapshot":%d}`, m.calls)), nil
}
func TestFieldsSnapshotOnceAcrossBothSinks(t *testing.T) {
	var wire, human bytes.Buffer
	fields := &changingMarshaler{}
	logger := New(&wire, &human)
	if err := logger.Emit(Event{Code: "snapshot", Fields: map[string]any{"custom": fields}}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fields.calls != 1 {
		t.Fatal("caller marshaler invoked more than once", fields.calls)
	}
	for _, output := range []string{wire.String(), human.String()} {
		if !strings.Contains(output, `"custom":{"snapshot":1}`) {
			t.Fatal("snapshots differ", output)
		}
	}
}

type invalidCountWriter int

func (w invalidCountWriter) Write(b []byte) (int, error) {
	if w < 0 {
		return -1, nil
	}
	return len(b) + 1, nil
}
func TestInvalidSinkCountIsReportedWithoutKillingWriter(t *testing.T) {
	for _, sink := range []invalidCountWriter{-1, 1} {
		logger := New(sink, nil)
		if err := logger.Emit(Event{Code: "bad-count"}); err != nil {
			t.Fatal(err)
		}
		if err := logger.Close(context.Background()); err == nil {
			t.Fatal("invalid sink accepted")
		}
		if logger.Status().Failed != 1 {
			t.Fatal(logger.Status())
		}
	}
}

func TestEmitPreservesEventJSONBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{name: "nil"},
		{name: "empty", fields: map[string]any{}},
		{name: "escaped", fields: map[string]any{
			"<key>":  "<>&\u2028\u2029\n\"\\诊断",
			"nested": map[string]any{"null": nil, "zero": 0, "false": false},
			"raw":    json.RawMessage(` { "html": "<>&", "number": 1.2300 } `),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Event{
				ID: "fixed-id", At: time.Date(2026, 11, 7, 1, 2, 3, 123, time.FixedZone("offset", 3600)),
				Level: "info", Module: "<module>", Code: "snapshot", Message: "<>&\u2028\n诊断",
				RequestID: "request", TraceID: "trace", SceneID: "scene", TurnID: "turn", Stage: "stage", DurationMS: 1.25,
				Fields: tc.fields,
			}
			want, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			var wire, human bytes.Buffer
			logger := New(&wire, &human)
			if err := logger.Emit(e); err != nil {
				t.Fatal(err)
			}
			if err := logger.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := wire.String(); got != string(want)+"\n" {
				t.Fatalf("JSON encoding changed:\nwant %s\n got %s", want, got)
			}
			if got, want := human.String(), Render(e)+"\n"; got != want {
				t.Fatalf("human encoding changed:\nwant %s\n got %s", want, got)
			}
		})
	}
}

func TestEmitSnapshotsMutableFieldsAndCountsStoredBytes(t *testing.T) {
	for _, withHuman := range []bool{false, true} {
		t.Run(fmt.Sprintf("human=%t", withHuman), func(t *testing.T) {
			sink := &slowSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
			var human bytes.Buffer
			var humanSink io.Writer
			if withHuman {
				humanSink = &human
			}
			logger := New(sink, humanSink)
			release := func() {
				select {
				case <-sink.release:
				default:
					close(sink.release)
				}
			}
			t.Cleanup(func() {
				release()
				if err := logger.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			values := []string{"before"}
			nested := map[string]any{"values": values}
			e := Event{ID: "fixed", At: time.Unix(1, 0), Code: "snapshot", Fields: map[string]any{"nested": nested}}
			wire, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			wantWire, wantHuman := string(wire)+"\n", Render(e)+"\n"
			bytesPerEntry := int64(len(wantWire))
			if withHuman {
				bytesPerEntry += int64(len(wantHuman))
			}
			if err := logger.Emit(e); err != nil {
				t.Fatal(err)
			}
			<-sink.entered
			if err := logger.Emit(e); err != nil {
				t.Fatal(err)
			}
			values[0] = "after"
			nested["extra"] = "new"
			e.Fields["new"] = true
			status := logger.Status()
			if status.InFlight != 1 || status.Queued != 1 || status.InFlightBytes != bytesPerEntry || status.QueuedBytes != bytesPerEntry {
				t.Fatalf("incorrect stored backlog bytes: %+v, want %d per entry", status, bytesPerEntry)
			}
			release()
			if err := logger.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := sink.String(); got != wantWire+wantWire {
				t.Fatalf("caller mutation changed JSON snapshot: %s", got)
			}
			if withHuman && human.String() != wantHuman+wantHuman {
				t.Fatalf("caller mutation changed human snapshot: %s", human.String())
			}
			status = logger.Status()
			if status.Written != 2 || status.Failed != 0 || status.InFlight != 0 || status.Queued != 0 || status.InFlightBytes != 0 || status.QueuedBytes != 0 {
				t.Fatalf("backlog did not drain: %+v", status)
			}
		})
	}
}

type callbackMarshaler func() ([]byte, error)

func (m callbackMarshaler) MarshalJSON() ([]byte, error) { return m() }

func TestFieldsMarshalerCanReenterLogger(t *testing.T) {
	var wire bytes.Buffer
	logger := New(&wire, nil)
	defer logger.Close(context.Background())
	calls := 0
	fields := callbackMarshaler(func() ([]byte, error) {
		calls++
		_ = logger.Status()
		if err := logger.Emit(Event{Code: "inner"}); err != nil {
			return nil, err
		}
		return []byte(`{"ok":true}`), nil
	})
	if err := logger.Emit(Event{Code: "outer", Fields: map[string]any{"callback": fields}}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || logger.Status().Written != 2 {
		t.Fatalf("reentrant marshaler behavior changed: calls=%d, status=%+v", calls, logger.Status())
	}
	if output := wire.String(); strings.Index(output, `"code":"inner"`) >= strings.Index(output, `"code":"outer"`) {
		t.Fatalf("reentrant event order changed: %s", output)
	}
}

type invalidOnceWriter struct {
	bytes.Buffer
	calls int
}

func (w *invalidOnceWriter) Write(b []byte) (int, error) {
	w.calls++
	if w.calls == 1 {
		return len(b) + 1, nil
	}
	return w.Buffer.Write(b)
}

func TestInvalidWriterCountStillDeliversOtherSinkAndLaterEvents(t *testing.T) {
	var wire invalidOnceWriter
	var human bytes.Buffer
	logger := New(&wire, &human)
	for _, code := range []string{"failed-json", "delivered-json"} {
		if err := logger.Emit(Event{Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(context.Background()); err == nil {
		t.Fatal("delivery error hidden")
	}
	status := logger.Status()
	if status.Failed != 1 || status.Written != 1 || status.LastError != io.ErrShortWrite.Error() || status.InFlight != 0 || status.InFlightBytes != 0 || status.Queued != 0 || status.QueuedBytes != 0 {
		t.Fatalf("unexpected delivery counters: %+v", status)
	}
	if wire.calls != 2 || !strings.Contains(wire.String(), `"code":"delivered-json"`) {
		t.Fatalf("later JSON event lost: %s", wire.String())
	}
	if strings.Count(human.String(), "\n") != 2 || !strings.Contains(human.String(), "failed-json") || !strings.Contains(human.String(), "delivered-json") {
		t.Fatalf("human event lost after JSON sink failure: %s", human.String())
	}
}
