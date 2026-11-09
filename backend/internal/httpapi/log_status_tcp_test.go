package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability/logdelivery"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestLogStatusSlowTCPDoesNotBlockWriterOrRevocation(t *testing.T) {
	_, main, _, gate := fixtureServer(t)
	writer, e := recovery.OpenWriter(context.Background(), gate.Authority)
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Close()
	logs := observability.NewStoreLogger(observability.StoreLoggerOptions{Database: ":memory:", Registry: logdelivery.NewRegistry(io.Discard)})
	defer logs.Close(context.Background())
	s, e := New(main, writer, fixtureToken, logs)
	if e != nil {
		t.Fatal(e)
	}
	p, e := recovery.NewReadPublisher(context.Background(), main, writer)
	if e != nil {
		t.Fatal(e)
	}
	defer p.CloseAndWait(context.Background())
	if e = s.WithLogStatus(p); e != nil {
		t.Fatal(e)
	}
	// A healthy lazy status cannot mutate or acquire an owner. This is the exact
	// immutable response accepted before concurrent authority changes.
	expected, e := json.Marshal(logs.LoggingStatus())
	if e != nil {
		t.Fatal(e)
	}
	tcp := httptest.NewUnstartedServer(s)
	pause := &pausedTCPListener{Listener: tcp.Listener, entered: make(chan struct{}), release: make(chan struct{})}
	tcp.Listener = pause
	tcp.Start()
	var once sync.Once
	unblock := func() { once.Do(func() { close(pause.release) }) }
	defer tcp.Close()
	defer unblock()
	client, e := net.Dial("tcp", tcp.Listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	if e = client.SetDeadline(time.Now().Add(15 * time.Second)); e != nil {
		t.Fatal(e)
	}
	if _, e = fmt.Fprintf(client, "GET /api/logs/status HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nX-Slow-Client: true\r\nConnection: close\r\n\r\n", fixtureToken); e != nil {
		t.Fatal(e)
	}
	select {
	case <-pause.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("TCP write not reached")
	}
	// A small response may reach this pause in net/http's final flush after the
	// handler returns. The proof here is transport independence, not app drain.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	head, e := writer.Write(ctx, main, []recovery.Revocation{recovery.Source{SceneID: "scene", MessageID: "status-concurrent-write"}}, recovery.ReaderCapability, func(m *recovery.MainMutation) error {
		_, e := m.Exec("INSERT OR REPLACE INTO config(key,value) VALUES('log-status-test','committed')")
		return e
	})
	if e != nil {
		t.Fatal("writer blocked by transport", e)
	}
	var value string
	if e = main.QueryRow("SELECT value FROM config WHERE key='log-status-test'").Scan(&value); e != nil || value != "committed" {
		t.Fatal("write did not commit", value, e)
	}
	second, e := recovery.OpenWriter(ctx, gate.Authority)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	if _, e = second.Append(ctx, recovery.Source{SceneID: "scene", MessageID: "status-revoked-after-enqueue"}, head, false); e != nil {
		t.Fatal("revocation blocked by transport", e)
	}
	request, e := http.NewRequest("GET", tcp.URL+"/api/logs/status", nil)
	if e != nil {
		t.Fatal(e)
	}
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	httpClient := tcp.Client()
	httpClient.Timeout = 5 * time.Second
	denied, e := httpClient.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(denied.Body)
	denied.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	var failure struct{ Error, RequestID string }
	if e = json.Unmarshal(body, &failure); e != nil {
		t.Fatal(e)
	}
	if denied.StatusCode != 503 || failure.Error != "Request failed; inspect server diagnostics" || failure.RequestID != denied.Header.Get("X-Request-ID") || failure.RequestID == "" || denied.Header.Get("Cache-Control") != "" || denied.Header.Get("Server-Timing") != "" {
		t.Fatal("post-revocation request escaped fence", denied.StatusCode, string(body), denied.Header)
	}
	unblock()
	res, e := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: "GET"})
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	body, e = io.ReadAll(res.Body)
	if e != nil || res.StatusCode != 200 || string(body) != string(expected) {
		t.Fatal("accepted status replayed/changed", res.StatusCode, string(body), e)
	}
}
