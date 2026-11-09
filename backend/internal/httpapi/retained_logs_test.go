package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func retainedFixture(t *testing.T) (*Server, *recovery.ReadPublisher) {
	t.Helper()
	s, db, a, _, _ := configurationFixture(t)
	s.logs = observability.New(nil, nil)
	t.Cleanup(func() { s.logs.Close(context.Background()) })
	p, err := recovery.NewReadPublisher(context.Background(), db, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.CloseAndWait(context.Background()) })
	if err = s.WithRetainedLogs(p); err != nil {
		t.Fatal(err)
	}
	s.token = "synthetic-token"
	return s, p
}
func retainedRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer synthetic-token")
	r.Header.Set("X-Request-ID", "synthetic-request")
	return r
}
func TestRetainedLogsActualSourceHTTP(t *testing.T) {
	data, err := os.ReadFile("testdata/retained-logs-http-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name, Path, Method, Text                 string
			Status, Count                            int
			Empty, BadAuth, NoAuth, NoToken, Failure bool
			ResponseHeaders                          map[string]*string
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			s, publisher := retainedFixture(t)
			count := c.Count
			if count == 0 && !c.Empty {
				count = 2
			}
			for i := 0; i < count; i++ {
				level := "info"
				if i%2 != 0 {
					level = "error"
				}
				err = s.logs.Emit(observability.Event{ID: fmt.Sprint("row-", i), At: time.Date(2026, 11, 9, 0, 0, 0, 123000000, time.UTC), Level: level, Module: "synthetic", Code: fmt.Sprint("retained-", i), Fields: map[string]any{"counts": map[string]any{"items": i}, "payload": "中文<&>"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := string(s.logs.RetainedJSON())
			r := retainedRequest(c.Method, c.Path)
			if c.BadAuth {
				r.Header.Set("Authorization", "Bearer wrong")
			}
			if c.NoAuth {
				r.Header.Del("Authorization")
			}
			if c.NoToken {
				s.token = ""
			}
			if c.Failure {
				if err = publisher.CloseAndWait(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != c.Status {
				t.Fatalf("got %d %s; want %d", w.Code, w.Body.String(), c.Status)
			}
			text := w.Body.String()
			if c.Failure {
				text = strings.ReplaceAll(text, "synthetic-request", "<request-id>")
			}
			got, want := any(text), any(c.Text)
			if w.Code == 200 && c.Method != "HEAD" {
				if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal([]byte(c.Text), &want); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v; want %v", got, want)
			}
			if !c.Failure && string(s.logs.RetainedJSON()) != before {
				t.Fatal("read or unsupported verb mutated logs")
			}
			for k, v := range c.ResponseHeaders {
				expected := ""
				if v != nil {
					expected = *v
				}
				if w.Header().Get(k) != expected {
					t.Errorf("%s: %q != %q", k, w.Header().Get(k), expected)
				}
			}
		})
	}
}
func TestRetainedLogsBindingAndUnavailable(t *testing.T) {
	s, p := retainedFixture(t)
	other, _ := retainedFixture(t)
	if other.WithRetainedLogs(p) == nil {
		t.Fatal("accepted foreign owner")
	}
	if s.WithRetainedLogs(nil) == nil {
		t.Fatal("accepted missing publisher")
	}
	var missing *Server
	if missing.WithRetainedLogs(p) == nil {
		t.Fatal("accepted nil server")
	}
	original := s.logs
	s.logs = nil
	if s.WithRetainedLogs(p) == nil {
		t.Fatal("accepted missing logger")
	}
	s.logs = original
	for _, method := range []string{"GET", "HEAD"} {
		w := httptest.NewRecorder()
		s.ServeUnavailable(w, retainedRequest(method, "/api/logs"))
		if w.Code != 503 || (method == "HEAD" && w.Body.Len() != 0) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if err := p.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, retainedRequest("GET", "/api/logs"))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "Request failed") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestRetainedLogsUnmigratedStatus(t *testing.T) {
	s, _ := retainedFixture(t)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, retainedRequest("GET", "/api/logs/status"))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "not yet migrated") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestRetainedLogsCanceledAndFailedTransport(t *testing.T) {
	s, _ := retainedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &trackingResponse{header: make(http.Header)}
	s.ServeHTTP(w, retainedRequest("GET", "/api/logs").WithContext(ctx))
	if w.statusCalls != 0 || w.writeCalls != 0 {
		t.Fatal("canceled read touched transport")
	}
	w = &trackingResponse{header: make(http.Header), short: true}
	s.ServeHTTP(w, retainedRequest("GET", "/api/logs"))
	if w.status != 200 || w.statusCalls != 1 || w.writeCalls != 1 {
		t.Fatal("partial output retried", w)
	}
}
