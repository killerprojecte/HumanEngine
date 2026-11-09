package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceadmission"
)

type memoryDeleteOracle struct {
	Node, Timezone        string
	SourceHashes, Schemas map[string]string
	Cases                 []struct {
		Name, Method, Path, ID, Text, ContentType string
		Authorization, ServerToken, Body          *string
		Repeat, Corrupt, Unready                  bool
		Before                                    map[string][]map[string]any
		Status                                    int
		Headers                                   map[string]string
	}
}

func memoryDeleteApplication(t *testing.T, token string) (*Application, *sql.DB) {
	t.Helper()
	options := applicationOptions(t)
	options.Entries = []string{}
	options.AdminToken = token
	a, err := Open(context.Background(), options)
	if err != nil {
		if a != nil {
			a.Shutdown(context.Background())
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return a, a.models.Client.Budgets.(*providertransport.BudgetService).DB
}

func TestApplicationMemoryDeleteActualSourceHTTP(t *testing.T) {
	raw, err := os.ReadFile("../httpapi/testdata/memory-delete-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture memoryDeleteOracle
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Node != "v24.22.1" || fixture.Timezone != "UTC" || len(fixture.Cases) != 32 {
		t.Fatal("oracle coverage/runtime", fixture.Node, fixture.Timezone, len(fixture.Cases))
	}
	for path, want := range fixture.SourceHashes {
		raw, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
			t.Fatal("source drift", path)
		}
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			token := "synthetic-token"
			if c.ServerToken != nil {
				token = *c.ServerToken
			}
			a, db := memoryDeleteApplication(t, token)
			// Replay only synthetic, actual-source owner prestates. The request runs
			// through the real application, privacy owners and response publisher.
			tables := []string{"memories", "memory_sources", "memory_supports", "memory_deleted", "memory_assertion_state", "memory_assertion_revisions", "memory_assertion_revision_sources", "memory_assertion_revision_parents", "memory_command_receipts"}
			known := map[string]bool{}
			for _, table := range tables {
				known[table] = true
			}
			for table := range c.Before {
				if !known[table] {
					t.Fatal("unhandled source prestate table", table)
				}
			}
			// Keep the installation's actual foreign-key enforcement enabled.
			// Parent tables precede children regardless of JSON/Go map order.
			for _, table := range tables {
				rows, present := c.Before[table]
				if !present {
					continue
				}
				var n int
				if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					if _, err := db.Exec(fixture.Schemas[table]); err != nil {
						t.Fatal(table, err)
					}
				}
				for _, row := range rows {
					keys, marks, args := []string{}, []string{}, []any{}
					for key := range row {
						keys = append(keys, key)
					}
					sort.Strings(keys)
					for _, key := range keys {
						marks = append(marks, "?")
						args = append(args, row[key])
					}
					if _, err := db.Exec("INSERT INTO "+table+"("+strings.Join(keys, ",")+") VALUES("+strings.Join(marks, ",")+")", args...); err != nil {
						t.Fatal(table, err)
					}
				}
			}
			if c.Corrupt {
				if _, err := db.Exec("UPDATE memories SET value='{' "); err != nil {
					t.Fatal(err)
				}
			}
			if c.Unready {
				var original string
				if err := db.QueryRow("SELECT value FROM recovery_gate WHERE id=1").Scan(&original); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE recovery_gate SET value=json_set(value,'$.minimumReader',?) WHERE id=1", recovery.ReaderCapability+1); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := db.Exec("UPDATE recovery_gate SET value=? WHERE id=1", original); err != nil {
						t.Error(err)
					}
				}()
			}
			tcp := httptest.NewServer(a)
			defer tcp.Close()
			request := func() *http.Response {
				var body io.Reader
				if c.Body != nil {
					body = strings.NewReader(*c.Body)
				}
				req, err := http.NewRequest(c.Method, tcp.URL+c.Path, body)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("X-Request-ID", "synthetic-memory-delete")
				if c.Name != "missing-auth" {
					auth := "Bearer synthetic-token"
					if c.Authorization != nil {
						auth = *c.Authorization
					}
					req.Header.Set("Authorization", auth)
				}
				if c.ContentType != "" {
					req.Header.Set("Content-Type", c.ContentType)
				}
				resp, err := tcp.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				return resp
			}
			if c.Repeat {
				first := request()
				body, err := io.ReadAll(first.Body)
				first.Body.Close()
				if err != nil || first.StatusCode != 200 || string(body) != `{"ok":true}` {
					t.Fatal("initial delete", first.StatusCode, string(body), err)
				}
			}
			resp := request()
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			requestID := resp.Header.Get("X-Request-ID")
			if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(requestID) || requestID == "synthetic-memory-delete" {
				t.Fatal("source fresh request ID", requestID)
			}
			body = []byte(strings.ReplaceAll(string(body), requestID, "<request-id>"))
			if err != nil || resp.StatusCode != c.Status || string(body) != c.Text {
				t.Fatalf("got %d %s (%v), want %d %s", resp.StatusCode, body, err, c.Status, c.Text)
			}
			for name, want := range c.Headers {
				got := resp.Header.Get(name)
				if name == "x-request-id" {
					got = "<request-id>"
				}
				if got != want {
					t.Errorf("header %s = %q want %q", name, got, want)
				}
			}
			if a.Bridge.Host.PID() != 0 {
				t.Fatal("deletion spawned model/Node")
			}
		})
	}
}

func TestApplicationMemoryDeleteLeavesOtherCRUDAtPreview(t *testing.T) {
	a, _ := memoryDeleteApplication(t, "synthetic-token")
	for _, operation := range []string{"GET /api/memories", "HEAD /api/memories", "POST /api/memories", "GET /api/memories/page", "HEAD /api/memories/page", "PATCH /api/memories/m", "POST /api/memory-commitments", "DELETE /api/memory-commitments/m"} {
		method, path, _ := strings.Cut(operation, " ")
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer synthetic-token")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "not yet migrated") {
			t.Fatal(operation, w.Code, w.Body.String())
		}
	}
	other, _ := memoryDeleteApplication(t, "synthetic-token")
	if err := a.HTTP.WithMemoryDelete(other.Semantic.Memory); err == nil {
		t.Fatal("different installation owner bound")
	}
	if err := a.HTTP.WithMemoryDelete(nil); err == nil {
		t.Fatal("nil privacy owner bound")
	}
}

// Source evidence is created by actual native transport ingress, read through
// retained-source authority, and admitted by the same app.Semantic.Memory owner.
// No raw memory INSERT or model admission substitute is used by this test.
func TestApplicationMemoryDeleteSourceAdmissionAndPhysicalPrivacy(t *testing.T) {
	a, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := context.Background()
	putObject := func(path string, object *jsjson.Object) {
		raw, err := jsjson.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, a, "PUT", path, json.RawMessage(raw))
	}
	settings := appRequest(t, a, "GET", "/api/settings", nil).(*jsjson.Object)
	settings.Set("sourceEventRevisionsEnabled", true)
	settings.Set("memoryProjectionEnabled", true)
	putObject("/api/settings", settings)
	scene := botruntime.Scene{}
	scene.ID, scene.Kind, scene.AdapterID, scene.AccountID, scene.PlatformSceneID = "synthetic-delete-scene", "group", "synthetic", "account", "room"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	message := memoryToolObject("id", "synthetic-source", "sceneId", scene.ID, "senderId", "synthetic-user", "sentAt", now, "receivedAt", now, "origin", "live", "status", "available", "parts", []any{memoryToolObject("type", "text", "text", "I prefer concise replies.")})
	if _, err := a.Engine.Runtime.IngestScene(ctx, scene, message, false); err != nil {
		t.Fatal(err)
	}
	scenes := appRequest(t, a, "GET", "/api/scenes", nil).([]any)
	var saved *jsjson.Object
	for _, s := range scenes {
		if field(s, "id") == scene.ID {
			saved = s.(*jsjson.Object)
		}
	}
	if saved == nil {
		t.Fatal("real ingress did not persist scene")
	}
	saved.Set("learningEnabled", true)
	putObject("/api/scenes/"+url.PathEscape(scene.ID), saved)
	var evidence []any
	err := a.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		trusted, e := a.Engine.Runtime.PreviewMessage(scene.ID, "synthetic-source", reads)
		if e != nil {
			return e
		}
		evidence, e = sourceadmission.TrustedEvidence([]*jsjson.Object{trusted}, func(_, id string) (bool, error) { return reads.IsRecalled(scene, id) })
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	input := memoryToolObject("sceneId", scene.ID, "simulation", false, "provenance", "source-only", "sourceEvidence", evidence)
	candidate := memoryToolObject("content", "Prefers concise replies.", "kind", "fact", "epistemicKind", "explicit", "sources", []any{memoryToolObject("sceneId", scene.ID, "messageId", "synthetic-source", "senderId", "synthetic-user")}, "subject", memoryToolObject("kind", "self", "sourceMessageId", "synthetic-source"))
	admitted, err := a.Semantic.Memory.Propose(ctx, input, candidate, memoryservice.ProposalOptions{})
	if err != nil || admitted.Status != "saved" {
		t.Fatal("real source admission", admitted, err)
	}
	id := field(admitted.Memory, "id").(string)
	if _, err = db.Exec("INSERT INTO vector_spaces VALUES('synthetic-delete','{}')"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO memory_vectors VALUES(?,?,?)", id, "synthetic-delete", []byte{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memory_fts", "memory_person_projection_state", "memory_assertion_revisions"} {
		var n int
		if err = db.QueryRow("SELECT count(*) FROM "+table+" WHERE assertion_id=?", id).Scan(&n); err != nil || n == 0 {
			t.Fatal("actual projection/history absent", table, n, err)
		}
	}
	tcp := httptest.NewServer(a)
	defer tcp.Close()
	deleteOverTCP := func() any {
		req, err := http.NewRequest("DELETE", tcp.URL+"/api/memories/"+url.PathEscape(id), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer synthetic-token")
		resp, err := tcp.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			t.Fatal("source-admitted TCP delete", resp.StatusCode, string(body), err)
		}
		value, err := jsjson.Parse(string(body))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	result := deleteOverTCP()
	if field(result, "ok") != true {
		t.Fatal(result)
	}
	for _, table := range []string{"memories", "memory_vectors", "memory_fts", "memory_person_projection_state", "memory_assertion_revisions"} {
		key := "assertion_id"
		if table == "memories" {
			key = "id"
		}
		if table == "memory_vectors" {
			key = "memory_id"
		}
		var n int
		if err = db.QueryRow("SELECT count(*) FROM "+table+" WHERE "+key+"=?", id).Scan(&n); err != nil || n != 0 {
			t.Fatal("retained deleted data", table, n, err)
		}
	}
	if field(deleteOverTCP(), "ok") != false {
		t.Fatal("repeat delete")
	}
	again, err := a.Semantic.Memory.Propose(ctx, input, candidate, memoryservice.ProposalOptions{})
	if err != nil || again.Status != "rejected" || again.Reason != "manually_deleted" {
		t.Fatal("fingerprint relearn fence", again, err)
	}
}

type memoryDeleteTCPListener struct {
	net.Listener
	once             sync.Once
	entered, release chan struct{}
}

func (l *memoryDeleteTCPListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	first := false
	l.once.Do(func() { first = true })
	if first {
		return &memoryDeleteTCPConn{Conn: c, entered: l.entered, release: l.release}, nil
	}
	return c, nil
}

type memoryDeleteTCPConn struct {
	net.Conn
	once             sync.Once
	entered, release chan struct{}
}

func (c *memoryDeleteTCPConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Conn.Write(b)
}

func TestApplicationMemoryDeleteHeldTCPReleasesWriters(t *testing.T) {
	a, db := memoryDeleteApplication(t, "synthetic-token")
	created, err := a.Semantic.Memory.Create(context.Background(), memoryToolObject("scope", memoryToolObject("kind", "sharedPublicKnowledge"), "content", "Synthetic held deletion.", "kind", "fact"))
	if err != nil {
		t.Fatal(err)
	}
	id := field(created, "id").(string)
	tcp := httptest.NewUnstartedServer(a)
	pause := &memoryDeleteTCPListener{Listener: tcp.Listener, entered: make(chan struct{}), release: make(chan struct{})}
	tcp.Listener = pause
	tcp.Start()
	var once sync.Once
	unblock := func() { once.Do(func() { close(pause.release) }) }
	defer tcp.Close()
	defer unblock()
	client, err := net.Dial("tcp", tcp.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err = fmt.Fprintf(client, "DELETE /api/memories/%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer synthetic-token\r\nConnection: close\r\n\r\n", url.PathEscape(id)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pause.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("never reached actual TCP write")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// This uses the actual same service's main pool and authority mutex.
	if _, err = a.Semantic.Memory.Create(ctx, memoryToolObject("scope", memoryToolObject("kind", "sharedPublicKnowledge"), "content", "Independent writer while TCP held.", "kind", "fact")); err != nil {
		t.Fatal("slow TCP retained writer", err)
	}
	gate, err := recovery.ReadGate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	external, err := recovery.OpenWriter(ctx, gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err = external.Append(ctx, recovery.Source{SceneID: "s", MessageID: "after-acceptance"}, gate.Head, false); err != nil {
		t.Fatal("slow TCP retained authority", err)
	}
	unblock()
	resp, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || string(body) != `{"ok":true}` {
		t.Fatal("accepted response rematerialized", resp.StatusCode, string(body), err)
	}
	var n int
	if err = db.QueryRow("SELECT count(*) FROM memories WHERE id=?", id).Scan(&n); err != nil || n != 0 {
		t.Fatal("delete did not remain committed", n, err)
	}
}

func TestApplicationMemoryDeleteShutdownDrainsAcceptedResponse(t *testing.T) {
	a, db := memoryDeleteApplication(t, "synthetic-token")
	w := &heldWriter{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(w.release) }) }
	defer unblock()
	r := httptest.NewRequest("DELETE", "/api/memories/missing", nil)
	r.Header.Set("Authorization", "Bearer synthetic-token")
	done := make(chan struct{})
	go func() { a.ServeHTTP(w, r); close(done) }()
	select {
	case <-w.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("handler response not accepted")
	}
	shutdown := make(chan error, 1)
	go func() { shutdown <- a.Shutdown(context.Background()) }()
	select {
	case err := <-shutdown:
		t.Fatal("shutdown escaped held handler response", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := db.Ping(); err != nil {
		t.Fatal("main closed before handler drain", err)
	}
	unblock()
	<-done
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}

// A small response may reach net.Conn.Write only in net/http's final flush,
// after the application handler has returned. The launcher owns both drains;
// this test preserves that boundary rather than extending application lifetime.
func TestApplicationMemoryDeleteCombinedTCPShutdown(t *testing.T) {
	a, _ := memoryDeleteApplication(t, "synthetic-token")
	tcp := httptest.NewUnstartedServer(a)
	pause := &memoryDeleteTCPListener{Listener: tcp.Listener, entered: make(chan struct{}), release: make(chan struct{})}
	tcp.Listener = pause
	tcp.Start()
	var once sync.Once
	unblock := func() { once.Do(func() { close(pause.release) }) }
	defer tcp.Close()
	defer unblock()
	client, err := net.Dial("tcp", tcp.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(15 * time.Second))
	if _, err = fmt.Fprint(client, "DELETE /api/memories/missing HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer synthetic-token\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pause.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("response not accepted at TCP")
	}
	shutdown := make(chan error, 1)
	go func() {
		httpDone := make(chan error, 1)
		go func() { httpDone <- tcp.Config.Shutdown(context.Background()) }()
		appErr := a.ShutdownAndFlush(context.Background())
		shutdown <- errors.Join(appErr, <-httpDone)
	}()
	select {
	case err := <-shutdown:
		t.Fatal("combined shutdown escaped held TCP response", err)
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	resp, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: "DELETE"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || string(body) != `{"ok":false}` {
		t.Fatal("accepted TCP response", resp.StatusCode, string(body), err)
	}
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("combined shutdown did not finish")
	}
}
