package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (*Authority, *sql.DB, *sql.DB, Gate) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	if e := os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	adb, e := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = adb.Exec(`CREATE TABLE identity (id TEXT PRIMARY KEY, format INTEGER NOT NULL); CREATE TABLE revocations (sequence INTEGER PRIMARY KEY, previous_hash TEXT NOT NULL, payload TEXT NOT NULL, hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if e != nil {
		t.Fatal(e)
	}
	main, e := sql.Open("sqlite3", filepath.Join(root, "main.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL); CREATE TABLE data(value TEXT);INSERT INTO data VALUES('original')`)
	if e != nil {
		t.Fatal(e)
	}
	gate := Gate{Head: Head{"12345678-1234-4123-8123-123456789012", 0, strings.Repeat("0", 64)}, Authority: path, MinimumReader: 11, State: "ready"}
	save(t, main, gate)
	readonly, e := storage.OpenExisting(filepath.Join(root, "main.sqlite"), false)
	if e != nil {
		t.Fatal(e)
	}
	a, e := Open(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close(); readonly.Close(); main.Close(); adb.Close() })
	return a, readonly, adb, gate
}
func save(t *testing.T, db *sql.DB, g Gate) {
	t.Helper()
	b, _ := json.Marshal(g)
	if _, e := db.Exec("INSERT OR REPLACE INTO recovery_gate VALUES(1,?)", string(b)); e != nil {
		t.Fatal(e)
	}
}
func TestWarmReadReusesAuditAndExternalMutationIsRechecked(t *testing.T) {
	a, main, external, g := fixture(t)
	ctx := context.Background()
	read := func(*sql.Conn) ([]byte, error) { return []byte("verified"), nil }
	emit := func([]byte) error { return nil }
	for range 10 {
		if e := a.Publish(ctx, main, read, emit); e != nil {
			t.Fatal(e)
		}
	}
	if a.Scans() != 1 {
		t.Fatal("repeated full scans", a.Scans())
	}
	payload := `{"kind":"source","sceneId":"s","messageId":"m"}`
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|1|%s|%s", g.Head.InstanceID, g.Head.Hash, payload)))
	if _, e := external.Exec("INSERT INTO revocations VALUES(1,?,?,?)", g.Head.Hash, payload, hex.EncodeToString(digest[:])); e != nil {
		t.Fatal(e)
	}
	called := false
	e := a.Publish(ctx, main, read, func([]byte) error { called = true; return nil })
	if e == nil || called {
		t.Fatal("stale main ACK escaped")
	}
	if a.Scans() != 2 {
		t.Fatal("external commit not audited")
	}
	if _, e = external.Exec("UPDATE revocations SET hash=? WHERE sequence=1", strings.Repeat("f", 64)); e != nil {
		t.Fatal(e)
	}
	if e = a.Publish(ctx, main, read, emit); e == nil || !strings.Contains(e.Error(), "chain_invalid") {
		t.Fatal("historical tamper accepted", e)
	}
}
func TestPublicationExcludesWriterAndReleasesAfterCallbackError(t *testing.T) {
	a, main, external, _ := fixture(t)
	_, _ = external.Exec("PRAGMA busy_timeout=0")
	err := a.Publish(context.Background(), main, func(*sql.Conn) ([]byte, error) { return []byte("x"), nil }, func([]byte) error {
		if _, e := external.Exec("BEGIN IMMEDIATE"); e == nil {
			_, _ = external.Exec("ROLLBACK")
			t.Fatal("writer reservation admitted during publication")
		}
		return fmt.Errorf("sink failed")
	})
	if err == nil {
		t.Fatal("callback failure lost")
	}
	if _, e := external.Exec("BEGIN IMMEDIATE"); e != nil {
		t.Fatal("reservation leaked", e)
	}
	_, _ = external.Exec("ROLLBACK")
}
func TestEligibilityValidation(t *testing.T) {
	good := `{"kind":"assertion_eligibility","schemaVersion":1,"operationId":"12345678-1234-4123-8123-123456789012","mode":"baseline","changes":[]}`
	if e := ValidateEvent(good); e != nil {
		t.Fatal(e)
	}
	if e := ValidateEvent(strings.Replace(good, `"baseline"`, `"transition"`, 1)); e == nil {
		t.Fatal("empty transition accepted")
	}
	if e := ValidateEvent(`{"kind":"future_unknown"}`); e == nil {
		t.Fatal("unknown domain accepted")
	}
}
