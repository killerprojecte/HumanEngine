package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

func TestStatusActualSourceHTTP(t *testing.T) {
	data, err := os.ReadFile("testdata/status-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Seed struct {
			Settings, Models json.RawMessage
			VectorVersion    string
		}
		Cases []struct {
			Name, Method, Path, Text string
			Error, BadAuth           bool
			Status                   int
			Headers                  map[string]*string
		}
	}
	if err = json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	for _, c := range source.Cases {
		t.Run(c.Name, func(t *testing.T) {
			s, db, a, _, _ := configurationFixture(t)
			s.token = "synthetic-token"
			for key, value := range map[string]json.RawMessage{"settings": source.Seed.Settings, "models": source.Seed.Models} {
				if _, err := db.Exec("INSERT OR REPLACE INTO config(key,value) VALUES(?,?)", key, string(value)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec("DELETE FROM scenes"); err != nil {
				t.Fatal(err)
			}
			logs := observability.New(nil, nil)
			defer logs.Close(context.Background())
			s.logs = logs
			if c.Error {
				if err := logs.Emit(observability.Event{Level: "error", Module: "synthetic", Code: "status_error"}); err != nil {
					t.Fatal(err)
				}
			}
			publisher, err := recovery.NewReadPublisher(context.Background(), db, a)
			if err != nil {
				t.Fatal(err)
			}
			defer publisher.CloseAndWait(context.Background())
			runtime := new(botruntime.Runtime)
			schedule := scheduler.NewModelScheduler()
			started := time.Now().Add(-2 * time.Second)
			if err = s.WithRuntimeStatus(runtime, schedule, publisher, source.Seed.VectorVersion, started); err != nil {
				t.Fatal(err)
			}
			method, path := c.Method, c.Path
			if method == "" {
				method = "GET"
			}
			if path == "" {
				path = "/api/status"
			}
			r := httptest.NewRequest(method, path, nil)
			token := "synthetic-token"
			if c.BadAuth {
				token = "wrong"
			}
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != c.Status {
				t.Fatalf("%d %s; source %d", w.Code, w.Body.String(), c.Status)
			}
			text := w.Body.String()
			if w.Code == 200 && method != "HEAD" {
				var got, want map[string]any
				if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got["startedAt"] != started.UTC().Format("2006-01-02T15:04:05.000Z") {
					t.Fatal(got)
				}
				if got["uptimeSeconds"].(float64) < 2 {
					t.Fatal(got)
				}
				got["startedAt"] = "<application-start>"
				got["uptimeSeconds"] = float64(0)
				if err = json.Unmarshal([]byte(c.Text), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %+v source %+v", got, want)
				}
			} else if text != c.Text {
				t.Fatalf("%s source %s", text, c.Text)
			}
			for key, want := range c.Headers {
				got := w.Header().Get(key)
				if want == nil {
					if got != "" {
						t.Errorf("%s=%q", key, got)
					}
				} else if got != *want {
					t.Errorf("%s=%q want %q", key, got, *want)
				}
			}
		})
	}
}

func TestStatusGetterRunsOutsidePublicationAndCancels(t *testing.T) {
	s, db, a, _, _ := configurationFixture(t)
	logs := observability.New(nil, nil)
	defer logs.Close(context.Background())
	s.logs = logs
	publisher, err := recovery.NewReadPublisher(context.Background(), db, a)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.CloseAndWait(context.Background())
	runtime := new(botruntime.Runtime)
	entered := make(chan struct{})
	var once sync.Once
	var disposed atomic.Bool
	runtime.AddAdapter("blocking", &botruntime.Adapter{Status: func(ctx context.Context) (string, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return "", ctx.Err()
	}, Dispose: func() { disposed.Store(true) }})
	if err = s.WithRuntimeStatus(runtime, scheduler.NewModelScheduler(), publisher, "synthetic-installed-version", time.Now()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest("GET", "/api/status", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer synthetic-config-token")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(response, request) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("getter never entered")
	}
	check, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err = publisher.Publish(check, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil }); err != nil {
		t.Fatal("getter held publication", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("status cancellation stuck")
	}
	if response.Body.Len() != 0 || disposed.Load() {
		t.Fatal("request cancellation published or disposed adapter")
	}
}
