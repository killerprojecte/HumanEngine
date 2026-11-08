package httpapi

import (
	"bufio"
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

func TestSlowExperienceTCPDoesNotBlockWriterOrWithdrawal(t *testing.T) {
	server, db, authority := experienceHTTP(t)
	seedExperienceHTTP(t, db, experienceFixture(t).Initial)
	// Validation check strings are intentionally unbounded in the source schema.
	marker := strings.Repeat("synthetic-before-", 8192)
	if _, e := db.Exec("UPDATE memory_experience_validations SET value=json_set(value,'$.checks',json_array(?))", marker); e != nil {
		t.Fatal(e)
	}
	tcp := httptest.NewUnstartedServer(server)
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
	client.SetDeadline(time.Now().Add(15 * time.Second))
	_, e = fmt.Fprintf(client, "GET /api/memory-experience?sceneId=s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n", fixtureToken)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-pause.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("never reached transport")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	head, e := authority.Write(ctx, db, []recovery.Revocation{recovery.Source{SceneID: "s", MessageID: "concurrent"}}, recovery.ReaderCapability, func(m *recovery.MainMutation) error {
		_, e := m.Exec("UPDATE config SET value=? WHERE key='identity:s:alice'", `{"senderDisplayName":"After"}`)
		return e
	})
	if e != nil {
		t.Fatal("slow response owns writer", e)
	}
	if _, e = authority.Append(ctx, recovery.Source{SceneID: "s", MessageID: "withdrawn-after-acceptance"}, head, false); e != nil {
		t.Fatal("slow response owns authority", e)
	}
	unblock()
	response, e := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: "GET"})
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(response.Body)
	response.Body.Close()
	if e != nil || response.StatusCode != 200 || !strings.Contains(string(body), marker) || !strings.Contains(string(body), `"personName":"Alice"`) || strings.Contains(string(body), `"personName":"After"`) {
		t.Fatal("accepted bytes changed", response.StatusCode, e)
	}
}
