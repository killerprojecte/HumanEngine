package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
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

func TestSlowHabitTCPDoesNotBlockWriterOrRevocation(t *testing.T) {
	server, main, writer, gate := habitHTTP(t)
	old := strings.Replace(habitRecord, "Synthetic", strings.Repeat("synthetic-before-", 8192), 1)
	if _, err := main.Exec("INSERT INTO habits VALUES('h','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',?)", old); err != nil {
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
	_, err = fmt.Fprintf(client, "GET /api/habits HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nX-Slow-Client: true\r\nConnection: close\r\n\r\n", fixtureToken)
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
		_, err := m.Exec("UPDATE habits SET value=? WHERE id='h'", `{"id":"scene","name":"after"}`)
		return err
	})
	if err != nil {
		t.Fatal("concurrent writer failed while TCP output was paused", err)
	}
	if head.Sequence != gate.Head.Sequence+1 {
		t.Fatal("writer did not commit authority")
	}
	var persisted string
	if err = main.QueryRow("SELECT value FROM habits WHERE id='h'").Scan(&persisted); err != nil || !strings.Contains(persisted, `"after"`) {
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
	status, body := readResponse(t, tcp.Client(), tcp.URL, "GET", "/api/habits", fixtureToken)
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
