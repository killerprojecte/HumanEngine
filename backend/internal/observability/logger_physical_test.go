package observability

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestRawPhysicalWaitDoesNotInitiateClose(t *testing.T) {
	l := New(io.Discard, io.Discard)
	defer l.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.WaitPhysicalClose(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if l.Status().Closed {
		t.Fatal("read-only physical wait initiated close")
	}
	if err := l.Emit(Event{Level: "info", Module: "synthetic", Code: "still-open"}); err != nil {
		t.Fatal(err)
	}
}
func TestRawPhysicalWaitPrefersDoneToCanceledContext(t *testing.T) {
	gate := make(chan struct{})
	writer := &storeOwnerWriter{gate: gate, entered: make(chan struct{})}
	l := New(writer, io.Discard)
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
		t.Fatal("held raw owner disappeared", err)
	}
	close(gate)
	join, cancelJoin := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelJoin()
	if err := l.WaitPhysicalClose(join); err != nil {
		t.Fatal(err)
	}
	if err := l.WaitPhysicalClose(ctx); err != nil {
		t.Fatal("completed Done lost cancellation race", err)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
