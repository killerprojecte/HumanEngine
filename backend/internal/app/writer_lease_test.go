//go:build linux

package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

func TestApplicationWriterLeaseRetainedUntilPhysicalDrain(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	a, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer a.ShutdownAndFlush(context.Background())
	writer := &heldWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(writer.release) }) }
	defer unblock()
	request := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	request.Header.Set("Authorization", "Bearer synthetic-token")
	done := make(chan struct{})
	go func() { a.ServeHTTP(writer, request); close(done) }()
	select {
	case <-writer.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("request not admitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = a.ShutdownAndFlush(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || a.finalized {
		t.Fatal("failed physical drain did not retain owner", err)
	}
	if options.Logs.Status().Closed {
		t.Fatal("failed drain closed the owned logger")
	}
	if err = options.Logs.Emit(observability.Event{Level: "info", Module: "test", Code: "writer_lease.retained", Message: "Synthetic retained-owner probe"}); err != nil {
		t.Fatal("logger no longer accepts after failed drain", err)
	}
	second, e := installation.Open(context.Background(), options.Installation)
	if second != nil {
		second.Close()
		t.Fatal("second writer admitted during failed drain")
	}
	if !errors.Is(e, installation.ErrWriterBusy) {
		t.Fatal("lease not retained", e)
	}
	unblock()
	<-done
	if err = a.ShutdownAndFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !options.Logs.Status().Closed {
		t.Fatal("successful shutdown did not close owned logger")
	}
	if e = options.Logs.Emit(observability.Event{Level: "info", Module: "test", Code: "writer_lease.closed", Message: "Synthetic closed-logger probe"}); !errors.Is(e, observability.ErrClosed) {
		t.Fatal("logger accepted after successful shutdown", e)
	}
	reopened, e := installation.Open(context.Background(), options.Installation)
	if e != nil {
		t.Fatal("clean drain failed to release lease", e)
	}
	if e = reopened.Close(); e != nil {
		t.Fatal(e)
	}
}
