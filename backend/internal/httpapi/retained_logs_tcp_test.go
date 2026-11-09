package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRetainedLogsSlowTCPDoesNotBlockWriterOrRevocation(t *testing.T) {
	_, main, _, gate := fixtureServer(t)
	writer, err := recovery.OpenWriter(context.Background(), gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	logs := observability.New(nil, nil)
	defer logs.Close(context.Background())
	server, err := New(main, writer, fixtureToken, logs)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := recovery.NewReadPublisher(context.Background(), main, writer)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.CloseAndWait(context.Background())
	if err = server.WithRetainedLogs(publisher); err != nil {
		t.Fatal(err)
	}
	if err = logs.Emit(observability.Event{Level: "info", Code: "synthetic-before", Fields: map[string]any{"payload": strings.Repeat("synthetic-before-", 8192)}}); err != nil {
		t.Fatal(err)
	}
	old := string(logs.RetainedJSON())
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
	_, err = fmt.Fprintf(client, "GET /api/logs HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nX-Slow-Client: true\r\nConnection: close\r\n\r\n", fixtureToken)
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
		_, err := m.Exec("INSERT OR REPLACE INTO config(key,value) VALUES('retained-test',?)", `{"id":"scene","name":"after"}`)
		return err
	})
	if err != nil {
		t.Fatal("concurrent writer failed while TCP output was paused", err)
	}
	if head.Sequence != gate.Head.Sequence+1 {
		t.Fatal("writer did not commit authority")
	}
	var persisted string
	if err = main.QueryRow("SELECT value FROM config WHERE key='retained-test'").Scan(&persisted); err != nil || !strings.Contains(persisted, `"after"`) {
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
	// The generic legacy readResponse helper requires success cache headers on
	// every response. Source onError omits them; assert its actual error contract
	// here without relaxing any paused-transport, writer or revocation assertion.
	request, err := http.NewRequest("GET", tcp.URL+"/api/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	request.Header.Set("X-Request-ID", "synthetic-request-id")
	denied, err := tcp.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(denied.Body)
	denied.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	status := denied.StatusCode
	for key, value := range map[string]string{"Content-Type": "application/json", "Cache-Control": "", "X-Content-Type-Options": "", "X-Request-ID": "synthetic-request-id"} {
		if denied.Header.Get(key) != value {
			t.Fatalf("source error header %s: %q != %q", key, denied.Header.Get(key), value)
		}
	}
	if string(body) != `{"error":"Request failed; inspect server diagnostics","requestId":"synthetic-request-id"}` {
		t.Fatalf("source error body: %s", body)
	}

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
	if err != nil || response.StatusCode != 200 || string(body) != old {
		t.Fatalf("accepted response changed after revocation: status=%d bytes=%d err=%v", response.StatusCode, len(body), err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
}
