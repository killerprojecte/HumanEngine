package sourceevents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestSourceFixedSnapshotWorkCounts(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()
	r, e := f.store.Observe(ctx, ingress())
	if e != nil {
		t.Fatal(e)
	}
	e = f.store.Read(ctx, func(v *View) error {
		query := v.query
		schemaReads := 0
		v.query = func(q string, args ...any) (Rows, error) {
			if strings.Contains(q, "sqlite_master") {
				schemaReads++
			}
			return query(q, args...)
		}
		for i := 0; i < 100; i++ {
			ref, e := v.Capture(fixtureIdentity(), strings.Repeat("a", 64), *r.Ref)
			if e != nil {
				return e
			}
			if ref == nil {
				t.Fatal("lost capture")
			}
		}
		if schemaReads != 1 {
			t.Fatal("schema work grew with captures", schemaReads)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestSourceHTTPDoesNotWaitForSubscriber(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()
	if _, e := f.store.Observe(ctx, ingress()); e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	f.store.SubscribeCommitted(&Listener{Handle: func(_ *Store, m Mutation) error {
		if m.Kind == "edited" {
			close(entered)
			<-release
		}
		return nil
	}})
	done := make(chan error, 1)
	go func() { _, e := f.store.Observe(ctx, edit(1)); done <- e }()
	<-entered
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := f.store.Read(r.Context(), func(v *View) error { _, e := v.Registered("scene", "message"); return e })
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	response, e := client.Get(server.URL)
	if e != nil {
		close(release)
		<-done
		t.Fatal("publication waited for subscriber", e)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 503 || !strings.Contains(string(body), "head_changed") {
		close(release)
		<-done
		t.Fatal(response.StatusCode, string(body))
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	response, e = client.Get(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
}
func TestSourceHundredThousandPriorIntents(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()
	gate, e := recovery.ReadGate(ctx, f.main)
	if e != nil {
		t.Fatal(e)
	}
	external, e := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer external.Close()
	tx, e := external.Begin()
	if e != nil {
		t.Fatal(e)
	}
	stmt, e := tx.Prepare("INSERT INTO revocations VALUES(?,?,?,?)")
	if e != nil {
		t.Fatal(e)
	}
	head := gate.Head
	for i := 0; i < 100000; i++ {
		payload := fmt.Sprintf(`{"kind":"source","sceneId":"history","messageId":"m-%d"}`, i)
		previous := head.Hash
		head.Sequence++
		hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", head.InstanceID, head.Sequence, previous, payload)))
		head.Hash = hex.EncodeToString(hash[:])
		if _, e = stmt.Exec(head.Sequence, previous, payload, head.Hash); e != nil {
			t.Fatal(e)
		}
	}
	stmt.Close()
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	gate.Head = head
	raw, _ := json.Marshal(gate)
	if _, e = f.main.Exec("UPDATE recovery_gate SET value=?", string(raw)); e != nil {
		t.Fatal(e)
	}
	if actual, e := f.authority.Head(ctx); e != nil || actual != head {
		t.Fatal(actual, e)
	}
	scans := f.authority.Scans()
	if _, e = f.store.Observe(ctx, ingress()); e != nil {
		t.Fatal(e)
	}
	for i := int64(1); i <= 8; i++ {
		r, e := f.store.Observe(ctx, edit(i))
		if e != nil {
			t.Fatal(e)
		}
		if e = f.store.Read(ctx, func(v *View) error {
			for j := 0; j < 10; j++ {
				result, e := v.Current(*r.Ref, fixtureIdentity())
				if e != nil {
					return e
				}
				if !result.Eligible {
					t.Fatal(result)
				}
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
	if got := f.authority.Scans(); got != scans {
		t.Fatalf("controlled source writes rescanned 100k history: %d -> %d", scans, got)
	}
}
