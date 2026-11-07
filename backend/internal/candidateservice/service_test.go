package candidateservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/httpapi"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Service, *sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	ap := filepath.Join(dir, "authority")
	if err := os.Mkdir(ap, 0700); err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(ap, "authority.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL); INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1); CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "data", "main.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL); CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE secrets(namespace TEXT,name TEXT,value TEXT,PRIMARY KEY(namespace,name)); CREATE TABLE recovery_security_state (id INTEGER PRIMARY KEY CHECK(id=1), revision TEXT NOT NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	gate, _ := json.Marshal(recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: ap, MinimumReader: 1, State: "ready"})
	if _, err = db.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(gate)); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, db, path
}
func get(t *testing.T, server *httptest.Server, method, path string) (int, string) {
	t.Helper()
	r, _ := http.NewRequest(method, server.URL+path, nil)
	r.Header.Set("Authorization", "Bearer test")
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	b, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(b)
}
func settings(t *testing.T) *jsjson.Object {
	t.Helper()
	v, err := jsjson.Parse(string(contract.DefaultSettingsInput()))
	if err != nil {
		t.Fatal(err)
	}
	return v.(*jsjson.Object)
}
func scene(t *testing.T, name string) *jsjson.Object {
	t.Helper()
	v, err := jsjson.Parse(`{"id":"scene","kind":"group","adapterId":"adapter","accountId":"bot","platformSceneId":"chat","name":"Fixture","participationEnabled":true,"learningEnabled":true,"proactiveEnabled":false}`)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*jsjson.Object)
	if err = o.Set("name", name); err != nil {
		t.Fatal(err)
	}
	return o
}
func TestSharedServiceTCPCommitsAndGap(t *testing.T) {
	s, db, _ := fixture(t)
	server := httptest.NewServer(s)
	defer server.Close()
	if code, _ := get(t, server, "GET", "/api/settings"); code != 200 {
		t.Fatal(code)
	}
	scans := s.authority.Scans()
	value := settings(t)
	if err := value.Set("name", "Shared writer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSettings(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if code, body := get(t, server, "GET", "/api/settings"); code != 200 || !strings.Contains(body, "Shared writer") {
		t.Fatalf("%d %s", code, body)
	}
	if _, err := s.SaveScene(context.Background(), scene(t, "Committed scene")); err != nil {
		t.Fatal(err)
	}
	if code, body := get(t, server, "GET", "/api/scenes"); code != 200 || !strings.Contains(body, "Committed scene") {
		t.Fatalf("%d %s", code, body)
	}
	if s.authority.Scans() != scans {
		t.Fatalf("shared writes rescanned: %d -> %d", scans, s.authority.Scans())
	}
	if code, body := get(t, server, "HEAD", "/api/scenes"); code != 200 || body != "" {
		t.Fatalf("HEAD %d %s", code, body)
	}
	if code, _ := get(t, server, "POST", "/api/settings"); code != 503 {
		t.Fatal(code)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_scene BEFORE UPDATE ON scenes BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	next := scene(t, "Uncommitted")
	if err := next.Set("accountId", "changed identity"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveScene(context.Background(), next); err == nil {
		t.Fatal("expected durable intent/main ACK gap")
	}
	if code, body := get(t, server, "GET", "/api/scenes"); code != 503 || strings.Contains(body, "Committed scene") {
		t.Fatalf("gap published: %d %s", code, body)
	}
}
func TestBorrowedHTTPAndOwnedClose(t *testing.T) {
	s, _, _ := fixture(t)
	borrowed, err := httpapi.New(s.main, s.authority, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = borrowed.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.SetSettings(context.Background(), settings(t)); err != nil {
		t.Fatal("borrower closed dependencies:", err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	if code, _ := get(t, server, "GET", "/api/settings"); code != 200 {
		t.Fatal(code)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err = s.main.Ping(); err == nil {
		t.Fatal("owned DB still open")
	}
	if _, err = s.SetSettings(context.Background(), settings(t)); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err = s.SaveScene(context.Background(), scene(t, "closed")); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if code, _ := get(t, server, "GET", "/api/settings"); code != 503 {
		t.Fatal(code)
	}
	if stats := s.main.Stats(); stats.OpenConnections != 0 {
		t.Fatalf("leaked pool: %+v", stats)
	}
}

type blockedWriter struct {
	header           http.Header
	entered, release chan struct{}
}

func (w *blockedWriter) Header() http.Header { return w.header }
func (w *blockedWriter) WriteHeader(int)     {}
func (w *blockedWriter) Write(b []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(b), nil
}
func TestCancellationAndCloseDrainPublication(t *testing.T) {
	s, _, _ := fixture(t)
	w := &blockedWriter{make(http.Header), make(chan struct{}), make(chan struct{})}
	r := httptest.NewRequest("GET", "/api/settings", nil)
	r.Header.Set("Authorization", "Bearer test")
	done := make(chan struct{})
	go func() { s.ServeHTTP(w, r); close(done) }()
	<-w.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.SetSettings(ctx, settings(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	entered, finished := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); s.ServeHTTP(w, r); close(finished) }))
	defer server.Close()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	request, _ := http.NewRequestWithContext(requestCtx, "GET", server.URL+"/api/settings", nil)
	request.Header.Set("Authorization", "Bearer test")
	requestDone := make(chan error, 1)
	go func() {
		response, err := server.Client().Do(request)
		if response != nil {
			response.Body.Close()
		}
		requestDone <- err
	}()
	<-entered
	cancelRequest()
	if err := <-requestDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("HTTP cancel: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled HTTP waiter did not release")
	}
	closing := make(chan error, 1)
	go func() { closing <- s.Close() }()
	select {
	case err := <-closing:
		t.Fatalf("closed during emission: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(w.release)
	<-done
	select {
	case err := <-closing:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close deadlocked")
	}
}
func TestOpenFailureDoesNotCloseCallerDatabase(t *testing.T) {
	_, db, path := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := Open(ctx, path, "test", nil); err == nil {
		s.Close()
		t.Fatal("canceled open succeeded")
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if s, err := httpapi.New(db, nil, "test", nil); err == nil {
		s.Close()
		t.Fatal("nil authority accepted")
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
}

func TestClosingRejectsAdmissionBeforeSlowEmissionDrains(t *testing.T) {
	s, _, _ := fixture(t)
	blocked := &blockedWriter{make(http.Header), make(chan struct{}), make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(blocked.release) })
	request := httptest.NewRequest("GET", "/api/settings", nil)
	request.Header.Set("Authorization", "Bearer test")
	emitted := make(chan struct{})
	go func() { s.ServeHTTP(blocked, request); close(emitted) }()
	<-blocked.entered
	closed := make(chan error, 2)
	go func() { closed <- s.Close() }()
	deadline := time.After(5 * time.Second)
	for {
		s.mu.Lock()
		closing := s.closed
		s.mu.Unlock()
		if closing {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Close did not stop admission")
		case <-time.After(time.Millisecond):
		}
	}
	go func() { closed <- s.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	value := settings(t)
	sceneValue := scene(t, "closing")
	writes := make(chan error, 2)
	go func() { _, err := s.SetSettings(ctx, value); writes <- err }()
	go func() { _, err := s.SaveScene(ctx, sceneValue); writes <- err }()
	for range 2 {
		select {
		case err := <-writes:
			if !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("write blocked behind draining Close")
		}
	}
	response := httptest.NewRecorder()
	canceledRequest := httptest.NewRequest("GET", "/api/settings", nil).WithContext(ctx)
	canceledRequest.Header.Set("Authorization", "Bearer test")
	returned := make(chan struct{})
	go func() { s.ServeHTTP(response, canceledRequest); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled HTTP request blocked behind draining Close")
	}
	if response.Code != 503 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("shutdown response: %+v", response)
	}
	// A fresh actual TCP request is also rejected while resources remain open and
	// the first response still owns authority. This cannot enter Publish.
	server := httptest.NewServer(s)
	defer server.Close()
	server.Client().Timeout = 5 * time.Second
	if code, _ := get(t, server, "GET", "/api/scenes"); code != 503 {
		t.Fatal(code)
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before emission drained: %v", err)
	default:
	}
	release.Do(func() { close(blocked.release) })
	<-emitted
	for range 2 {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent Close failed to finish")
		}
	}
}
