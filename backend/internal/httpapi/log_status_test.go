package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
)

func TestLogStatusBindingUsesActualSourceOwner(t *testing.T) {
	s, p := retainedFixture(t)
	if s.WithLogStatus(p) == nil {
		t.Fatal("raw logger masqueraded as source owner")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, retainedRequest("GET", "/api/logs/status"))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	directory := filepath.Join(t.TempDir(), "not-created", "main.sqlite")
	logs := observability.NewStoreLogger(observability.StoreLoggerOptions{Database: directory, Registry: logdelivery.NewRegistry(io.Discard), LookupEnv: func(string) (string, bool) { return "", false }})
	t.Cleanup(func() { logs.Close(context.Background()); logs.WaitPhysicalClose(context.Background()) })
	s.logs = logs
	if e := s.WithLogStatus(p); e != nil {
		t.Fatal(e)
	}
	if s.logStatus.store != logs {
		t.Fatal("bound different source owner")
	}
	other, _ := retainedFixture(t)
	other.logs = logs
	if other.WithLogStatus(p) == nil {
		t.Fatal("foreign database/authority accepted")
	}
	if s.WithLogStatus(nil) == nil {
		t.Fatal("missing publisher accepted")
	}
	var missing *Server
	if missing.WithLogStatus(p) == nil {
		t.Fatal("nil server accepted")
	}
	var nilStore *observability.StoreLogger
	other.logs = nilStore
	if other.WithLogStatus(p) == nil {
		t.Fatal("typed nil accepted")
	}
	for _, method := range []string{"GET", "HEAD"} {
		w = httptest.NewRecorder()
		s.ServeHTTP(w, retainedRequest(method, "/api/logs/status"))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
		if method == "GET" {
			var status observability.StoreLoggingStatus
			if e := json.Unmarshal(w.Body.Bytes(), &status); e != nil {
				t.Fatal(e)
			}
			if status.File != nil || status.Stdout != nil || status.ShutdownComplete != nil {
				t.Fatal("lazy read acquired a lease", status)
			}
		}
	}
	if _, e := os.Stat(filepath.Dir(directory)); !os.IsNotExist(e) {
		t.Fatal("lazy status touched filesystem", e)
	}
	if logs.Retention().Records != 0 {
		t.Fatal("healthy status retained rows")
	}
}
func TestLogStatusCanceledAndFailedTransport(t *testing.T) {
	s, p := retainedFixture(t)
	logs := observability.NewStoreLogger(observability.StoreLoggerOptions{Database: ":memory:", Registry: logdelivery.NewRegistry(io.Discard)})
	t.Cleanup(func() { logs.Close(context.Background()); logs.WaitPhysicalClose(context.Background()) })
	s.logs = logs
	if e := s.WithLogStatus(p); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &trackingResponse{header: make(map[string][]string)}
	s.ServeHTTP(w, retainedRequest("GET", "/api/logs/status").WithContext(ctx))
	if w.statusCalls != 0 || w.writeCalls != 0 {
		t.Fatal("canceled request touched transport")
	}
	w = &trackingResponse{header: make(map[string][]string), short: true}
	s.ServeHTTP(w, retainedRequest("GET", "/api/logs/status"))
	if w.status != 200 || w.statusCalls != 1 || w.writeCalls != 1 {
		t.Fatal("accepted partial status was replayed", w)
	}
}
