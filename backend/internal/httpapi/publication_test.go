package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/mattn/go-sqlite3"
)

func TestPublicationResponseOwnsBytesAndConsumesOnce(t *testing.T) {
	header := http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"original"}}
	p := newPublicationResponse(header, false)
	body := []byte(`{"value":"before"}`)
	if err := p.enqueue(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	copy(body, strings.Repeat("x", len(body)))
	header.Set("X-Request-ID", "changed")
	if err := p.enqueue(context.Background(), []byte("replacement")); err == nil {
		t.Fatal("accepted twice")
	}
	w := httptest.NewRecorder()
	if err := p.drain(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Body.String() != `{"value":"before"}` || w.Header().Get("X-Request-ID") != "original" {
		t.Fatalf("response ownership lost: %d %s %v", w.Code, w.Body, w.Header())
	}
	if err := p.drain(context.Background(), w); err == nil {
		t.Fatal("replayed output")
	}
	if p.body != "" || p.header != nil {
		t.Fatal("consumed response retained bytes")
	}
}

func TestPublicationCancellationAndHEAD(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := newPublicationResponse(nil, false)
	if err := p.enqueue(ctx, []byte("private")); !errors.Is(err, context.Canceled) || p.accepted {
		t.Fatal("canceled enqueue", err)
	}
	if err := p.drain(context.Background(), httptest.NewRecorder()); err == nil {
		t.Fatal("unaccepted response drained")
	}
	for _, head := range []bool{false, true} {
		p = newPublicationResponse(nil, head)
		if err := p.enqueue(context.Background(), []byte("private")); err != nil {
			t.Fatal(err)
		}
		w := &trackingResponse{header: make(http.Header)}
		if err := p.drain(ctx, w); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if w.statusCalls != 0 || w.writeCalls != 0 {
			t.Fatal("canceled drain touched transport")
		}
		if err := p.drain(context.Background(), w); err == nil {
			t.Fatal("canceled response replayed")
		}
	}
	p = newPublicationResponse(nil, true)
	if err := p.enqueue(context.Background(), []byte("validated but suppressed")); err != nil {
		t.Fatal(err)
	}
	w := &trackingResponse{header: make(http.Header)}
	if err := p.drain(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if w.statusCalls != 1 || w.status != 200 || w.writeCalls != 0 {
		t.Fatal("HEAD wrote a body")
	}
}

type trackingResponse struct {
	header                          http.Header
	status, statusCalls, writeCalls int
	failure                         error
	short                           bool
}

func (w *trackingResponse) Header() http.Header    { return w.header }
func (w *trackingResponse) WriteHeader(status int) { w.status = status; w.statusCalls++ }
func (w *trackingResponse) Write(b []byte) (int, error) {
	w.writeCalls++
	if w.failure != nil {
		return 1, w.failure
	}
	if w.short {
		return len(b) - 1, nil
	}
	return len(b), nil
}

func TestHTTPPublicationTransportFailureNeverRetries(t *testing.T) {
	server, _, external, _ := fixtureServer(t)
	for _, short := range []bool{false, true} {
		w := &trackingResponse{header: make(http.Header), short: short}
		if !short {
			w.failure = errors.New("synthetic transport failure")
		}
		request := httptest.NewRequest("GET", "/api/settings", nil)
		request.Header.Set("Authorization", "Bearer "+fixtureToken)
		server.ServeHTTP(w, request)
		if w.status != 200 || w.statusCalls != 1 || w.writeCalls != 1 {
			t.Fatalf("failed output retried: %+v", w)
		}
		conn, err := external.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
		if err == nil {
			_, err = conn.ExecContext(context.Background(), "ROLLBACK")
		}
		conn.Close()
		if err != nil {
			t.Fatal("transport failure retained authority reservation", err)
		}
	}
	p := newPublicationResponse(nil, false)
	if err := p.enqueue(context.Background(), []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := p.drain(context.Background(), &trackingResponse{header: make(http.Header), short: true}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write lost", err)
	}
}

// Pause at the actual TCP connection's first write, below net/http's response
// buffering. The client does not read until both writers have finished. This
// deterministic transport barrier avoids sleeps or reliance on OS buffer sizes;
// HTTP parsing, chunking, handler lifetime, SQLite and the final bytes are real.
type pausedTCPListener struct {
	net.Listener
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (l *pausedTCPListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	first := false
	l.once.Do(func() { first = true })
	if first {
		return &pausedTCPConn{Conn: conn, entered: l.entered, release: l.release}, nil
	}
	return conn, nil
}

type pausedTCPConn struct {
	net.Conn
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (c *pausedTCPConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Conn.Write(b)
}

func TestSlowTCPPublicationDoesNotBlockWriterOrRevocation(t *testing.T) {
	preview, main, _, gate := fixtureServer(t)
	writer, err := recovery.OpenWriter(context.Background(), gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	// Borrow the same coordinator as the real writer: proves both the Go mutex
	// and SQLite reservation are released. A second coordinator below also
	// tests the cross-connection authority boundary.
	server, err := New(preview.main, writer, fixtureToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := `{"id":"scene","name":"` + strings.Repeat("synthetic-before-", 8192) + `"}`
	if _, err = main.Exec("INSERT INTO scenes VALUES('scene',?)", old); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var handlerOnce sync.Once
	tcp := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeHTTP(w, r)
		if r.Header.Get("X-Slow-Client") == "true" {
			handlerOnce.Do(func() { close(done) })
		}
	}))
	pause := &pausedTCPListener{Listener: tcp.Listener, entered: make(chan struct{}), release: make(chan struct{})}
	tcp.Listener = pause
	tcp.Start()
	var resume sync.Once
	unblock := func() { resume.Do(func() { close(pause.release) }) }
	defer tcp.Close()
	defer unblock()
	client, err := net.Dial("tcp", tcp.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(client, "GET /api/scenes HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nX-Slow-Client: true\r\nConnection: close\r\n\r\n", fixtureToken)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-pause.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("HTTP never reached TCP write")
	}
	select {
	case <-done:
		t.Fatal("large response unexpectedly bypassed paused transport")
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	head, err := writer.Write(ctx, main, []recovery.Revocation{recovery.Source{SceneID: "scene", MessageID: "concurrent-write"}}, recovery.ReaderCapability, func(m *recovery.MainMutation) error {
		_, err := m.Exec("UPDATE scenes SET value=? WHERE id='scene'", `{"id":"scene","name":"after"}`)
		return err
	})
	if err != nil {
		t.Fatal("concurrent writer failed while TCP output was paused", err)
	}
	if head.Sequence != gate.Head.Sequence+1 {
		t.Fatal("writer did not commit authority")
	}
	var persisted string
	if err = main.QueryRow("SELECT value FROM scenes WHERE id='scene'").Scan(&persisted); err != nil || !strings.Contains(persisted, `"after"`) {
		t.Fatal("writer did not commit main", err)
	}
	second, err := recovery.OpenWriter(ctx, gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err = second.Append(ctx, recovery.Source{SceneID: "scene", MessageID: "revoked-after-enqueue"}, head, false); err != nil {
		t.Fatal("concurrent authority withdrawal failed while TCP output was paused", err)
	}
	status, body := readResponse(t, tcp.Client(), tcp.URL, "GET", "/api/scenes", fixtureToken)
	if status != 503 || bytes.Contains(body, []byte("synthetic-before")) {
		t.Fatal("post-revocation request published old data", status)
	}
	select {
	case <-done:
		t.Fatal("transport stopped being paused")
	default:
	}
	unblock()
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(body) != "["+old+"]" {
		t.Fatalf("accepted response changed after revocation: status=%d bytes=%d err=%v", response.StatusCode, len(body), err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
}

func denyNextMainRollback(t *testing.T, main *sql.DB) *sqlite3.SQLiteConn {
	t.Helper()
	main.SetMaxOpenConns(1)
	conn, err := main.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var original *sqlite3.SQLiteConn
	err = conn.Raw(func(raw any) error {
		original = raw.(*sqlite3.SQLiteConn)
		original.RegisterAuthorizer(func(op int, first, second, database string) int {
			if op == sqlite3.SQLITE_TRANSACTION && first == "ROLLBACK" {
				return sqlite3.SQLITE_DENY
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	})
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	return original
}

func TestAcceptedHTTPPublicationSurvivesCleanupFailure(t *testing.T) {
	for _, handler := range []bool{false, true} {
		t.Run(fmt.Sprintf("handler=%v", handler), func(t *testing.T) {
			server, _, external, _ := fixtureServer(t)
			original := denyNextMainRollback(t, server.main)
			w := httptest.NewRecorder()
			if handler {
				request := httptest.NewRequest("GET", "/api/scenes", nil)
				request.Header.Set("Authorization", "Bearer "+fixtureToken)
				server.ServeHTTP(w, request)
			} else {
				p := newPublicationResponse(nil, false)
				err := server.publishResponse(context.Background(), "/api/scenes", p)
				if err == nil || !strings.Contains(err.Error(), "cleanup_failed") || !p.accepted {
					t.Fatal("cleanup failure not distinguished from acceptance", err)
				}
				if err = p.drain(context.Background(), w); err != nil {
					t.Fatal(err)
				}
				if err = p.drain(context.Background(), w); err == nil {
					t.Fatal("cleanup error permitted replay")
				}
			}
			if w.Code != 200 || w.Body.String() != "[]" {
				t.Fatalf("accepted response replaced after rollback failure: %d %s", w.Code, w.Body)
			}
			conn, err := server.main.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = conn.Raw(func(raw any) error {
				if raw.(*sqlite3.SQLiteConn) == original {
					return errors.New("poisoned connection reused")
				}
				return nil
			})
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = external.Exec("UPDATE identity SET format=format"); err != nil {
				t.Fatal("cleanup failure retained authority reservation", err)
			}
			// A replacement main connection may be used; the poisoned one must
			// not be. Future publication still revalidates the authority/gate.
			p := newPublicationResponse(nil, false)
			if err = server.publishResponse(context.Background(), "/api/scenes", p); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFailedProjectionNeverAcceptsOutput(t *testing.T) {
	server, main, _, _ := fixtureServer(t)
	if _, err := main.Exec("INSERT INTO config VALUES('models',?)", `{"private":"MUST_NOT_LEAK"`); err != nil {
		t.Fatal(err)
	}
	p := newPublicationResponse(nil, false)
	if err := server.publishResponse(context.Background(), "/api/models", p); err == nil || p.accepted {
		t.Fatal("bad projection accepted", err)
	}
	w := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/models", nil)
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	server.ServeHTTP(w, request)
	if w.Code != 503 || strings.Contains(w.Body.String(), "MUST_NOT_LEAK") {
		t.Fatal("pre-enqueue failure leaked projection")
	}
}
