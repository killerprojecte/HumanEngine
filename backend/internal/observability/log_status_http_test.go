package observability_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/httpapi"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Native SQLite/ReadPublisher fixture; only the platform recovery layout is
// seeded. Every delivery/status counter comes from the real Store and leases.
func logStatusHTTPServer(t *testing.T, logs *observability.StoreLogger, token string) (*httpapi.Server, *recovery.ReadPublisher, *sql.DB, *recovery.Authority, recovery.Gate) {
	t.Helper()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	if err := os.Mkdir(authorityPath, 0700); err != nil {
		t.Fatal(err)
	}
	db, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if closeErr := db.Close(); e != nil || closeErr != nil {
		t.Fatal(e, closeErr)
	}
	live := filepath.Join(root, "live")
	if e = os.Mkdir(live, 0700); e != nil {
		t.Fatal(e)
	}
	main, e := sql.Open("sqlite3", filepath.Join(live, "main.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { main.Close() })
	if _, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`); e != nil {
		t.Fatal(e)
	}
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: recovery.ReaderCapability, State: "ready"}
	raw, e := json.Marshal(gate)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	authority, e := recovery.OpenWriter(context.Background(), authorityPath)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { authority.Close() })
	publisher, e := recovery.NewReadPublisher(context.Background(), main, authority)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { publisher.CloseAndWait(context.Background()) })
	server, e := httpapi.New(main, authority, token, logs)
	if e != nil {
		t.Fatal(e)
	}
	if e = server.WithLogStatus(publisher); e != nil {
		t.Fatal(e)
	}
	return server, publisher, main, authority, gate
}
func statusFixture(t *testing.T, directory string) *observability.LogStatusFixtureForTest {
	t.Helper()
	f, e := observability.NewLogStatusFixtureForTest(directory)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = f.Store.Close(context.Background())
		if e := f.Store.WaitPhysicalClose(context.Background()); e != nil {
			t.Error(e)
		}
	})
	return f
}
func statusRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer synthetic-token")
	r.Header.Set("X-Request-ID", "synthetic-request")
	return r
}

var statusRequestID = regexp.MustCompile(`"requestId":"[^"]+"`)

func TestLogStatusActualSourceHTTP(t *testing.T) {
	raw, e := os.ReadFile("../httpapi/testdata/log-status-http-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var oracle struct {
		Cases []struct {
			Name, Path, Method, Text                                                 string
			Status, AddedRows, Notices                                               int
			BadAuth, NoAuth, NoToken, Failure, GetterFailure, Degraded, Closed, File bool
			ResponseHeaders                                                          map[string]*string
		}
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	if len(oracle.Cases) != 24 {
		t.Fatal("source case count", len(oracle.Cases))
	}
	headerRaw, e := os.ReadFile("../httpapi/testdata/log-status-headers-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var headerOracle struct {
		Cases []struct {
			Name, Path, Method, RequestIdShape string
			Status                             int
			CallerRequestIdIgnored             bool
			ResponseHeaders                    map[string]*string
			ServerTiming                       *string
			ErrorRequestIdMatchesHeader        *bool
		}
	}
	if e = json.Unmarshal(headerRaw, &headerOracle); e != nil {
		t.Fatal(e)
	}
	if len(headerOracle.Cases) != len(oracle.Cases) {
		t.Fatal("supplemental header case count")
	}
	timingPattern := regexp.MustCompile(`^recovery;dur=\d+\.\d{2}, handler;dur=\d+\.\d{2}, total;dur=\d+\.\d{2}$`)
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for index, c := range oracle.Cases {
		t.Run(c.Name, func(t *testing.T) {
			directory := ""
			if c.File {
				directory = filepath.Join(t.TempDir(), "not-created")
			}
			f := statusFixture(t, directory)
			token := "synthetic-token"
			if c.NoToken {
				token = ""
			}
			s, p, _, _, _ := logStatusHTTPServer(t, f.Store, token)
			if c.Degraded && f.Stdout.Enqueue(strings.Repeat("x", 1024*1024+1), true) {
				t.Fatal("oversized source admission accepted")
			}
			if c.Closed {
				if e = f.Store.Close(context.Background()); e != nil {
					t.Fatal(e)
				}
				if e = f.Store.WaitPhysicalClose(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			if c.Failure {
				if e = p.CloseAndWait(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			if c.GetterFailure {
				f.FailNextStatus()
			}
			before := f.Store.Retention().Records
			f.ResetStatusReads()
			r := statusRequest(c.Method, c.Path)
			if c.BadAuth {
				r.Header.Set("Authorization", "Bearer wrong")
			}
			if c.NoAuth {
				r.Header.Del("Authorization")
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			headers := headerOracle.Cases[index]
			if headers.Name != c.Name || headers.Path != c.Path || headers.Method != c.Method || headers.Status != w.Code {
				t.Fatal("header oracle case metadata", headers)
			}
			id := w.Header().Get("X-Request-ID")
			shape := id
			if uuidPattern.MatchString(id) {
				shape = "<uuid-v4>"
			}
			if shape != headers.RequestIdShape || (id != "synthetic-request") != headers.CallerRequestIdIgnored {
				t.Fatal("source request id", id)
			}
			timing := w.Header().Get("Server-Timing")
			if headers.ServerTiming == nil {
				if timing != "" {
					t.Fatal("unexpected timing", timing)
				}
			} else if !timingPattern.MatchString(timing) || regexp.MustCompile(`;dur=\d+\.\d{2}`).ReplaceAllString(timing, ";dur=<duration>") != *headers.ServerTiming {
				t.Fatal("source timing shape", timing)
			}
			for key, value := range headers.ResponseHeaders {
				want := ""
				if value != nil {
					want = *value
				}
				if w.Header().Get(key) != want {
					t.Fatal("supplemental response header", key, w.Header().Get(key), want)
				}
			}
			if headers.ErrorRequestIdMatchesHeader != nil {
				var body struct {
					RequestID string `json:"requestId"`
				}
				if e = json.Unmarshal(w.Body.Bytes(), &body); e != nil {
					t.Fatal(e)
				}
				if !*headers.ErrorRequestIdMatchesHeader || body.RequestID != id {
					t.Fatal("error/header request identity mismatch")
				}
			}
			got := statusRequestID.ReplaceAllString(w.Body.String(), `"requestId":"<request-id>"`)
			if w.Code != c.Status || got != c.Text {
				t.Fatalf("status/body mismatch: got%d %s; want%d %s", w.Code, got, c.Status, c.Text)
			}
			for key, value := range c.ResponseHeaders {
				want := ""
				if value != nil {
					want = *value
				}
				if w.Header().Get(key) != want {
					t.Errorf("header%s=%q want%q", key, w.Header().Get(key), want)
				}
			}
			after := f.Store.Retention().Records
			var rows []struct{ Code string }
			if e = json.Unmarshal(f.Store.RetainedJSON(), &rows); e != nil {
				t.Fatal(e)
			}
			notices := 0
			for _, row := range rows {
				if row.Code == "log_delivery_degraded" {
					notices++
				}
			}
			if after-before != c.AddedRows || notices != c.Notices {
				t.Fatal("retained side effects", after-before, notices, c.AddedRows, c.Notices)
			}
			// Two Status reads form one healthy Store getter: notice check then snapshot.
			// Early readiness failure emits one error (one notice check), without getter.
			reads := int64(0)
			if c.Failure {
				reads = 1
			} else if c.GetterFailure {
				reads = 2
			} else if c.Status == 200 {
				reads = 2
			}
			if f.StatusReads() != reads {
				t.Fatal("status getter execution boundary", f.StatusReads(), reads)
			}
			if directory != "" {
				if _, e = os.Stat(directory); !os.IsNotExist(e) {
					t.Fatal("status read created file directory", e)
				}
			}
		})
	}
}

func TestLogStatusCanceledAfterGetterDoesNotRepeatNotice(t *testing.T) {
	f := statusFixture(t, "")
	s, _, _, _, _ := logStatusHTTPServer(t, f.Store, "synthetic-token")
	if f.Stdout.Enqueue(strings.Repeat("x", 1024*1024+1), true) {
		t.Fatal("oversized admitted")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.HoldNextStatus(entered, release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(w, statusRequest("GET", "/api/logs/status").WithContext(ctx)) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("getter not reached")
	}
	cancel()
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled getter did not settle")
	}
	if w.Body.Len() != 0 {
		t.Fatal("canceled request emitted status", w.Body.String())
	}
	// Two reads in the one getter, followed by one in its failure diagnostic's
	// retained-notice check. A second getter would add another two reads.
	if f.StatusReads() != 3 {
		t.Fatal("getter repeated after cancellation", f.StatusReads())
	}
	var rows []struct{ Code string }
	if e := json.Unmarshal(f.Store.RetainedJSON(), &rows); e != nil {
		t.Fatal(e)
	}
	notices := 0
	for _, row := range rows {
		if row.Code == "log_delivery_degraded" {
			notices++
		}
	}
	if notices != 1 {
		t.Fatal("notice rolled back or repeated", notices)
	}
	before := f.Store.Retention().Records
	for _, method := range []string{"GET", "HEAD"} {
		w = httptest.NewRecorder()
		s.ServeHTTP(w, statusRequest(method, "/api/logs/status"))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if f.Store.Retention().Records != before {
		t.Fatal("repeat inside60s added notice")
	}
	f.Advance(60 * time.Second)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, statusRequest("HEAD", "/api/logs/status"))
	if w.Code != 200 || w.Body.Len() != 0 || f.Store.Retention().Records != before+1 {
		t.Fatal("60s HEAD notice boundary", w.Code, f.Store.Retention().Records)
	}
}

func TestLogStatusGetterFailureReleasesPublisherBeforeDiagnostic(t *testing.T) {
	f := statusFixture(t, "")
	s, _, main, authority, gate := logStatusHTTPServer(t, f.Store, "synthetic-token")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.FailThenHoldStatus(entered, release)
	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() { defer close(done); s.ServeHTTP(w, statusRequest("GET", "/api/logs/status")) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("failure diagnostic not reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	head, e := authority.Write(ctx, main, []recovery.Revocation{recovery.Source{SceneID: "scene", MessageID: "error-log-does-not-hold-authority"}}, recovery.ReaderCapability, func(*recovery.MainMutation) error { return nil })
	if e != nil || head.Sequence != gate.Head.Sequence+1 {
		t.Fatal("failure log still owns publisher reservation", head, e)
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failed getter did not settle")
	}
	if w.Code != 503 || f.StatusReads() != 2 {
		t.Fatal("failed getter retried", w.Code, f.StatusReads())
	}
	if strings.Contains(string(f.Store.RetainedJSON()), "synthetic_private_getter_failure") || strings.Contains(w.Body.String(), "synthetic_private_getter_failure") {
		t.Fatal("raw getter failure escaped")
	}
}
