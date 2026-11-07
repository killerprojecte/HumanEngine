package observability

import (
	"bytes"
	"context"
	"errors"
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
