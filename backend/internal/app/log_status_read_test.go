package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

func statusAppTCP(t *testing.T, client *http.Client, base, method, path, token string) (int, []byte, http.Header) {
	t.Helper()
	r, e := http.NewRequest(method, base+path, nil)
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Request-ID", "synthetic-client-id")
	response, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	body, e := io.ReadAll(response.Body)
	if e != nil {
		t.Fatal(e)
	}
	return response.StatusCode, body, response.Header
}
func TestApplicationLogStatusTCPUsesDefaultOwner(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	if options.Logs != nil {
		t.Fatal("fixture bypassed default logger")
	}
	a, e := Open(context.Background(), options)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		e := a.Shutdown(context.Background())
		if e != nil && !errors.Is(e, observability.ErrDeliveryIncomplete) {
			t.Error(e)
		}
	}()
	logs := sourceApplicationLogger(t, a)
	tcp := httptest.NewServer(a)
	defer tcp.Close()
	client := tcp.Client()
	client.Timeout = 10 * time.Second
	for _, method := range []string{"GET", "HEAD"} {
		code, body, headers := statusAppTCP(t, client, tcp.URL, method, "/api/logs/%73tatus?clear=true", "synthetic-token")
		if code != 200 || (method == "HEAD" && len(body) != 0) {
			t.Fatal(code, string(body))
		}
		if headers.Get("X-Request-ID") == "synthetic-client-id" || headers.Get("Server-Timing") == "" {
			t.Fatal("source headers", headers)
		}
		if method == "GET" {
			var status observability.StoreLoggingStatus
			if e = json.Unmarshal(body, &status); e != nil {
				t.Fatal(e)
			}
			if status.File == nil || status.Stdout == nil || status.Memory.RetainedRecords != logs.Retention().Records || status.Closed {
				t.Fatal("not actual default owner", status)
			}
		}
	}
	// An additional real lease degrades the same canonical stdout channel after
	// startup, without calling Store.Emit and pre-consuming its retained notice.
	lease, e := options.LogRegistry.Acquire("")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _, _ = lease.Close(context.Background()); _ = lease.WaitPhysicalRelease(context.Background()) }()
	accepted := lease.Status().AcceptedRecords
	if lease.Enqueue(strings.Repeat("x", 1024*1024+1), true) {
		t.Fatal("oversized real stdout accepted")
	}
	before := logs.Retention().Records
	code, _, _ := statusAppTCP(t, client, tcp.URL, "GET", "/api/logs/status", "wrong")
	if code != 401 || logs.Retention().Records != before {
		t.Fatal("auth read owner/notice", code)
	}
	code, body, _ := statusAppTCP(t, client, tcp.URL, "GET", "/api/logs/status", "synthetic-token")
	var status observability.StoreLoggingStatus
	if e = json.Unmarshal(body, &status); e != nil {
		t.Fatal(e)
	}
	if code != 200 || status.Stdout == nil || status.Stdout.RejectedRecords != 1 || status.Stdout.LastFailure == nil || *status.Stdout.LastFailure != "record_too_large" || status.Memory.RetainedRecords != before+1 {
		t.Fatal("not real degraded source status", code, status)
	}
	if status.Stdout.AcceptedRecords != accepted || status.File.AcceptedRecords != accepted {
		t.Fatal("notice leaked into sinks", status)
	}
	code, body, _ = statusAppTCP(t, client, tcp.URL, "GET", "/api/logs", "synthetic-token")
	if code != 200 || strings.Count(string(body), `"code":"log_delivery_degraded"`) != 1 {
		t.Fatal("retained notice absent/duplicated", code, string(body))
	}
	if a.Bridge.Host.PID() != 0 {
		t.Fatal("status route spawned Node")
	}
}
func TestApplicationLogStatusBorrowedRawTCPRemainsUnavailable(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	raw := observability.New(io.Discard, io.Discard)
	options.Logs = raw
	defer raw.Close(context.Background())
	a, e := Open(context.Background(), options)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Shutdown(context.Background())
	tcp := httptest.NewServer(a)
	defer tcp.Close()
	code, body, _ := statusAppTCP(t, tcp.Client(), tcp.URL, "GET", "/api/logs/status", "synthetic-token")
	if code != 503 || !strings.Contains(string(body), "not yet migrated") {
		t.Fatal("raw projection fabricated", code, string(body))
	}
	if e = a.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	if raw.Status().Closed {
		t.Fatal("borrowed raw closed")
	}
}
func TestApplicationLogStatusHeldResponseRetainsInstallation(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	a, e := Open(context.Background(), options)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Shutdown(context.Background())
	w := &heldWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(w.release) }) }
	defer unblock()
	done := make(chan struct{})
	r := httptest.NewRequest("GET", "/api/logs/status", nil)
	r.Header.Set("Authorization", "Bearer synthetic-token")
	go func() { a.ServeHTTP(w, r); close(done) }()
	loggingSignal(t, w.entered)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	e = a.Shutdown(ctx)
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		unblock()
		loggingSignal(t, done)
		t.Fatal("accepted response escaped shutdown", e)
	}
	assertApplicationWriterHeld(t, options)
	if sourceApplicationLogger(t, a).LoggingStatus().Closed {
		unblock()
		loggingSignal(t, done)
		t.Fatal("logger closed before HTTP drain")
	}
	unblock()
	loggingSignal(t, done)
	if e = a.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	assertApplicationWriterReleased(t, options)
}
