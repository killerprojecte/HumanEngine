package externalcost

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Real disk admission + real domain replay, not the test-only Maintenance stub.
// The authority contains the exact source-oracle events, including cases whose
// individual events are valid but whose revision progression must fail replay.
func TestAdmittedOfflineReplaySourceCases(t *testing.T) {
	f := load(t)
	for name, test := range f.Cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "authority")
			must(t, os.Mkdir(path, 0700))
			must(t, os.Mkdir(filepath.Join(root, "stage"), 0700))
			adb, e := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
			must(t, e)
			defer adb.Close()
			instance := "12345678-1234-4123-8123-123456789012"
			_, e = adb.Exec(`CREATE TABLE identity (id TEXT PRIMARY KEY, format INTEGER NOT NULL); CREATE TABLE revocations (sequence INTEGER PRIMARY KEY, previous_hash TEXT NOT NULL, payload TEXT NOT NULL, hash TEXT NOT NULL);INSERT INTO identity VALUES(?,1)`, instance)
			must(t, e)
			previous := strings.Repeat("0", 64)
			for i, payload := range test.Events {
				sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", instance, i+1, previous, payload)))
				hash := hex.EncodeToString(sum[:])
				_, e = adb.Exec("INSERT INTO revocations VALUES(?,?,?,?)", i+1, previous, payload, hash)
				must(t, e)
				previous = hash
			}
			a, e := recovery.Open(ctx, path)
			must(t, e)
			defer a.Close()
			database := filepath.Join(root, "stage", "main.sqlite")
			db, e := sql.Open("sqlite3", database)
			must(t, e)
			defer db.Close()
			g := recovery.Gate{Head: recovery.Head{InstanceID: instance, Hash: strings.Repeat("0", 64)}, Authority: path, MinimumReader: 3, State: "quarantine"}
			raw, e := json.Marshal(g)
			must(t, e)
			_, e = db.Exec("CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT);INSERT INTO recovery_gate VALUES(1,?)", string(raw))
			must(t, e)
			conn, e := db.Conn(ctx)
			must(t, e)
			defer conn.Close()
			admitted, e := recovery.AdmitMaintenance(ctx, conn, database, a)
			must(t, e)
			m := NewOfflineMaintenance(admitted)
			e = ReplayOffline(ctx, m, test.Events)
			if test.Error == "" {
				must(t, e)
			} else if e == nil {
				t.Fatal("expected source replay error")
			} else if strings.Contains(test.Error, "UNIQUE") {
				if !strings.Contains(e.Error(), "UNIQUE") {
					t.Fatal(e)
				}
			} else if e.Error() != test.Error {
				t.Fatalf("got %v source %s", e, test.Error)
			}
			rows, e := conn.QueryContext(ctx, "SELECT payload,payload_hash FROM "+Table+" ORDER BY revision")
			must(t, e)
			i := 0
			for rows.Next() {
				var payload, hash string
				must(t, rows.Scan(&payload, &hash))
				if i >= len(test.Rows) || test.Rows[i].Payload != payload || test.Rows[i].PayloadHash != hash {
					t.Fatal("source rows differ")
				}
				i++
			}
			must(t, rows.Err())
			must(t, rows.Close())
			if i != len(test.Rows) {
				t.Fatal(i, len(test.Rows))
			}
			var value string
			must(t, conn.QueryRowContext(ctx, "SELECT value FROM recovery_gate WHERE id=1").Scan(&value))
			var after recovery.Gate
			must(t, json.Unmarshal([]byte(value), &after))
			g.MinimumReader = 10
			if after != g {
				t.Fatalf("global gate changed: %+v", after)
			}
			if a.Scans() != 1 {
				t.Fatal("repeated full audits", a.Scans())
			}
		})
	}
}
