package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This is a work-count regression, not a timing benchmark. It proves cache
// behavior under controlled writes and explicitly retains the external-writer
// re-audit boundary. The HTTP handler exercises Publish, not the complete API.
func TestLongHistoryControlledWritesReuseAuditedPublication(t *testing.T) {
	f := newWriterFixture(t)
	ctx := context.Background()
	head := f.gate.Head
	tx, err := f.external.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare("INSERT INTO revocations(sequence,previous_hash,payload,hash) VALUES(?,?,?,?)")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100000; i++ {
		payload := fmt.Sprintf(`{"kind":"source","sceneId":"history","messageId":"m-%d"}`, i)
		previous := head.Hash
		head.Sequence++
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", head.InstanceID, head.Sequence, previous, payload)))
		head.Hash = hex.EncodeToString(digest[:])
		if _, err = statement.Exec(head.Sequence, previous, payload, head.Hash); err != nil {
			t.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.gate.Head = head
	save(t, f.main, f.gate)
	if actual, err := f.authority.Head(ctx); err != nil || actual != head {
		t.Fatal(actual, err)
	}
	f.authority.mu.Lock()
	initialScans := f.authority.scans
	f.authority.mu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := f.authority.Publish(r.Context(), f.main, func(conn *sql.Conn) ([]byte, error) {
			var v string
			err := conn.QueryRowContext(r.Context(), "SELECT value FROM data").Scan(&v)
			return []byte(v), err
		}, func(body []byte) error { _, err := w.Write(body); return err })
		if err != nil {
			http.Error(w, err.Error(), 503)
		}
	}))
	defer server.Close()
	read := func(want string) {
		t.Helper()
		response, err := http.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(body) != want {
			t.Fatalf("HTTP publication: %d %q %v", response.StatusCode, body, err)
		}
	}
	for i := 0; i < 4; i++ {
		value := fmt.Sprintf("own-%d", i)
		if _, err = f.authority.Write(ctx, f.main, []Revocation{Source{"live", value}}, 1, func(m *MainMutation) error { _, err := m.Exec("UPDATE data SET value=?", value); return err }); err != nil {
			t.Fatal(err)
		}
		read(value)
		read(value)
	}
	f.authority.mu.Lock()
	ownScans := f.authority.scans
	f.authority.mu.Unlock()
	if ownScans != initialScans {
		t.Fatalf("own writes re-audited history: %d -> %d", initialScans, ownScans)
	}
	// The payload dedup lookup is indexed rather than scanning all history.
	var id, parent, unused int
	var detail string
	if err = f.external.QueryRow("EXPLAIN QUERY PLAN SELECT 1 FROM revocations WHERE payload=?", "probe").Scan(&id, &parent, &unused, &detail); err != nil || !strings.Contains(detail, "revocations_payload") {
		t.Fatal(detail, err)
	}
	second, err := OpenWriter(ctx, f.gate.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err = second.Write(ctx, f.main, []Revocation{Source{"external", "writer"}}, 1, func(m *MainMutation) error { _, err := m.Exec("UPDATE data SET value='external'"); return err }); err != nil {
		t.Fatal(err)
	}
	read("external")
	read("external")
	f.authority.mu.Lock()
	externalScans := f.authority.scans
	f.authority.mu.Unlock()
	if externalScans != initialScans+1 {
		t.Fatalf("external change must be audited exactly once: %d -> %d", initialScans, externalScans)
	}
}
