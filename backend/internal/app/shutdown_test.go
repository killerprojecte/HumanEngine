package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
)

type heldWriter struct {
	header           http.Header
	entered, release chan struct{}
	once             sync.Once
}

func (w *heldWriter) Header() http.Header { return w.header }
func (w *heldWriter) WriteHeader(int)     {}
func (w *heldWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

type stageSink struct {
	atHTTP chan struct{}
	once   sync.Once
}

func (s *stageSink) Write(p []byte) (int, error) {
	if strings.Contains(string(p), `"code":"app.shutdown_wait"`) && strings.Contains(string(p), `"stage":"http"`) {
		s.once.Do(func() { close(s.atHTTP) })
	}
	return len(p), nil
}

func TestApplicationUncancelledShutdownReachesHeldHTTPBeforeDatabaseClose(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	stages := &stageSink{atHTTP: make(chan struct{})}
	logs := observability.New(stages, io.Discard)
	defer logs.Close(context.Background())
	options.Logs = logs
	app, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	writer := &heldWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(writer.release) }) }
	defer unblock()
	request := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	request.Header.Set("Authorization", "Bearer synthetic-token")
	responseDone := make(chan struct{})
	go func() { app.ServeHTTP(writer, request); close(responseDone) }()
	<-writer.entered
	finished := make(chan error, 1)
	go func() { finished <- app.Shutdown(context.Background()) }()
	select {
	case <-stages.atHTTP:
	case <-time.After(10 * time.Second):
		t.Fatal("did not reach actual HTTP drain stage")
	}
	select {
	case err := <-finished:
		t.Fatal("uncancelled shutdown escaped accepted HTTP", err)
	default:
	}
	database := app.models.Client.Budgets.(*providertransport.BudgetService).DB
	if err = database.Ping(); err != nil {
		t.Fatal("database closed before HTTP release", err)
	}
	unblock()
	<-responseDone
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if err = database.Ping(); err == nil {
		t.Fatal("installation stayed open after complete shutdown")
	}
}
