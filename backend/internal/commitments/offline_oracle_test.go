package commitments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadOfflineOracle(t *testing.T) *Object {
	t.Helper()
	raw, e := os.ReadFile("testdata/offline-source-oracle.json")
	must(t, e)
	return object(parse(t, string(raw)))
}

type offlineQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func offlineRows(t *testing.T, q offlineQuery, query string, args ...any) []any {
	t.Helper()
	rows, e := q.QueryContext(context.Background(), query, args...)
	must(t, e)
	defer rows.Close()
	columns, e := rows.Columns()
	must(t, e)
	out := []any{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		must(t, rows.Scan(dest...))
		o := new(Object)
		for i, k := range columns {
			v := values[i]
			if n, ok := v.(int64); ok {
				v = float64(n)
			}
			if b, ok := v.([]byte); ok {
				data := make([]any, len(b))
				for i, n := range b {
					data[i] = float64(n)
				}
				v = obj("type", "Buffer", "data", data)
			}
			_ = o.Set(k, v)
		}
		out = append(out, o)
	}
	must(t, rows.Err())
	return out
}
func offlineTables(t *testing.T, q offlineQuery) *Object {
	t.Helper()
	out := new(Object)
	names := offlineRows(t, q, "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'memory_commitment_%' ORDER BY name")
	for _, n := range names {
		name := str(get(n, "name"))
		columns := offlineRows(t, q, `PRAGMA table_xinfo("`+name+`")`)
		order := "1"
		if len(columns) > 1 {
			order += ",2"
		}
		_ = out.Set(name, offlineRows(t, q, `SELECT * FROM "`+name+`" ORDER BY `+order))
	}
	return out
}
func offlineSchema(t *testing.T, q offlineQuery) []any {
	return offlineRows(t, q, "SELECT type,name,sql FROM sqlite_master WHERE name LIKE 'memory_commitment_%' AND sql IS NOT NULL ORDER BY rowid")
}
func offlineBind(v any) any {
	if get(v, "type") == "Buffer" {
		data := arr(get(v, "data"))
		b := make([]byte, len(data))
		for i, n := range data {
			b[i] = byte(num(n))
		}
		return b
	}
	return v
}
func seedOfflineSource(t *testing.T, f *fixture, scenario *Object) {
	t.Helper()
	for _, v := range arr(get(scenario, "initialSchema")) {
		_, e := f.main.Exec(str(get(v, "sql")))
		must(t, e)
	}
	initial := object(parse(t, str(get(scenario, "initial"))))
	for _, table := range initial.Members() {
		for _, v := range arr(table.Value) {
			row := object(v)
			columns, marks := []string{}, []string{}
			args := []any{}
			for _, member := range row.Members() {
				columns = append(columns, member.Key)
				marks = append(marks, "?")
				args = append(args, offlineBind(member.Value))
			}
			_, e := f.main.Exec(`INSERT INTO "`+table.Key+`" (`+strings.Join(columns, ",")+") VALUES("+strings.Join(marks, ",")+")", args...)
			must(t, e)
		}
	}
}
func mainPath(t *testing.T, db *sql.DB) string {
	t.Helper()
	for _, v := range offlineRows(t, db, "PRAGMA database_list") {
		if get(v, "name") == "main" {
			return str(get(v, "file"))
		}
	}
	t.Fatal("no main path")
	return ""
}

// Use SQLite's page backup, like better-sqlite3's db.backup. VACUUM INTO
// reconstructs sqlite_master and moves indexes after tables, losing the source
// scan order which assertSupported deliberately observes without ORDER BY.
func backupOfflineMain(t *testing.T, source *sql.DB, path string) {
	t.Helper()
	ctx := context.Background()
	target, e := sql.Open("sqlite3", path)
	must(t, e)
	defer target.Close()
	src, e := source.Conn(ctx)
	must(t, e)
	defer src.Close()
	dst, e := target.Conn(ctx)
	must(t, e)
	defer dst.Close()
	must(t, dst.Raw(func(rawTarget any) error {
		return src.Raw(func(rawSource any) error {
			backup, e := rawTarget.(*sqlite3.SQLiteConn).Backup("main", rawSource.(*sqlite3.SQLiteConn), "main")
			if e != nil {
				return e
			}
			done, e := backup.Step(-1)
			finish := backup.Finish()
			if e = errors.Join(e, finish); e != nil {
				return e
			}
			if !done {
				return errors.New("uncontended fixture backup did not complete")
			}
			return nil
		})
	}))
	if stringify(t, offlineSchema(t, src)) != stringify(t, offlineSchema(t, dst)) {
		t.Fatal("fixture page backup changed schema order")
	}
}

func applyOfflineLive(t *testing.T, f *fixture, operations []any) {
	t.Helper()
	for _, op := range operations {
		must(t, f.store.Transaction(context.Background(), func(store *Store) error {
			s, _ := NewPrivacyService(store, f.service.owners)
			return store.WithWriteSession(func(session *recovery.WriteSession) error {
				switch get(op, "kind") {
				case "source":
					message := str(get(op, "message"))
					if _, e := session.AppendIntent(recovery.Source{SceneID: "scene", MessageID: message}); e != nil {
						return e
					}
					return s.PurgeSource("scene", message)
				case "assertion":
					id := str(get(op, "id"))
					if _, e := session.AppendIntent(recovery.Assertion{ID: id, ThroughVersion: 1, Terminal: true}); e != nil {
						return e
					}
					return s.PurgeAssertion(id)
				case "delete":
					return s.EraseRetained(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
				}
				return fmt.Errorf("unknown offline fixture live operation")
			})
		}))
	}
}
func TestActualSourceOlderBackupDomainReplay(t *testing.T) {
	oracle := loadOfflineOracle(t)
	for _, v := range arr(get(oracle, "scenarios")) {
		scenario := object(v)
		t.Run(str(get(scenario, "name")), func(t *testing.T) {
			f := setup(t)
			seedOfflineSource(t, f, scenario)
			root := filepath.Dir(filepath.Dir(mainPath(t, f.main)))
			backup := filepath.Join(root, "older.sqlite")
			backupOfflineMain(t, f.main, backup)
			applyOfflineLive(t, f, arr(get(scenario, "liveOps")))
			if actual := stringify(t, offlineTables(t, f.main)); actual != get(scenario, "liveRows") {
				t.Fatalf("live pre-restore mutation differs\n%s\n%s", actual, get(scenario, "liveRows"))
			}
			entries := offlineRows(t, f.external, "SELECT sequence,previous_hash,payload,hash FROM revocations ORDER BY sequence")
			if stringify(t, entries) != get(scenario, "entries") {
				t.Fatalf("original durable events differ\n%s\n%s", stringify(t, entries), get(scenario, "entries"))
			}
			events := []string{}
			for _, entry := range entries {
				events = append(events, str(get(entry, "payload")))
			}
			must(t, os.Mkdir(filepath.Join(root, "staging"), 0700))
			stagingPath := filepath.Join(root, "staging", "main.sqlite")
			bytes, e := os.ReadFile(backup)
			must(t, e)
			must(t, os.WriteFile(stagingPath, bytes, 0600))
			db, e := sql.Open("sqlite3", stagingPath)
			must(t, e)
			defer db.Close()
			db.SetMaxOpenConns(1)
			var gateRaw string
			must(t, db.QueryRow("SELECT value FROM recovery_gate").Scan(&gateRaw))
			gate := object(parse(t, gateRaw))
			_ = gate.Set("state", "quarantine")
			_, e = db.Exec("UPDATE recovery_gate SET value=?", stringify(t, gate))
			must(t, e)
			conn, e := db.Conn(context.Background())
			must(t, e)
			defer conn.Close()
			maintenance, e := recovery.AdmitMaintenance(context.Background(), conn, stagingPath, f.ledger)
			must(t, e)
			owners := []any{}
			fault := ""
			options := f.store.options
			options.Now = func() string { t.Fatal("offline replay called live clock"); return "" }
			port, e := NewOfflineMaintenance(maintenance, options, Owners{AfterWrite: func(store *Store, head *Object, next, prior []any) error {
				if e := store.WithMaintenanceScope(func(tx *recovery.MaintenanceTx) error { return tx.CheckScope(context.Background()) }); e != nil {
					return e
				}
				owners = append(owners, []any{"afterWrite", head, next, prior})
				if fault == "afterWrite" {
					return fmt.Errorf("synthetic_afterWrite")
				}
				return nil
			}})
			must(t, e)
			rawEvent := func(op *Object, kind string) string {
				if has(op, "raw") {
					return str(get(op, "raw"))
				}
				for _, raw := range events {
					if get(parse(t, raw), "kind") == kind {
						return raw
					}
				}
				t.Fatalf("missing %s fixture event", kind)
				return ""
			}
			for i, v := range arr(get(scenario, "steps")) {
				step := object(v)
				op := object(parse(t, str(get(step, "op"))))
				fault = str(get(op, "fault"))
				owners = []any{}
				if has(op, "sql") {
					_, e = conn.ExecContext(context.Background(), str(get(op, "sql")))
					must(t, e)
				}
				if get(op, "outerRollback") == true {
					_, e = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
					must(t, e)
				}
				var result any
				switch get(op, "kind") {
				case "assertSupported":
					result, e = port.AssertSupported(context.Background())
				case "recall":
					scene := "scene"
					if has(op, "scene") {
						scene = str(get(op, "scene"))
					}
					e = port.Recall(context.Background(), scene, str(get(op, "message")))
				case "purgeAssertion":
					e = port.PurgeAssertion(context.Background(), str(get(op, "id")))
				case "erase":
					e = port.RecoverErase(context.Background(), rawEvent(op, "commitment_delete"))
				case "marker":
					e = port.ReplayMarker(context.Background(), rawEvent(op, "commitment_head"))
				case "verify":
					selected := events
					if has(op, "events") {
						selected = []string{}
						for _, raw := range arr(get(op, "events")) {
							selected = append(selected, str(raw))
						}
					}
					e = port.Verify(context.Background(), selected)
				default:
					t.Fatal("unknown offline step")
				}
				if get(op, "outerRollback") == true {
					_, rollback := conn.ExecContext(context.Background(), "ROLLBACK")
					must(t, rollback)
				}
				want := get(step, "error")
				if want == nil && e != nil {
					t.Fatalf("step %d: %v", i, e)
				}
				if want != nil && (e == nil || !strings.Contains(e.Error(), str(want))) {
					t.Fatalf("step %d error %v want %v", i, e, want)
				}
				if e != nil {
					result = nil
				}
				check := func(label string, actual any, want string) {
					t.Helper()
					got := stringify(t, actual)
					if got != want {
						t.Fatalf("step %d %s mismatch\n%s\n%s", i, label, got, want)
					}
				}
				check("result", result, stringify(t, get(step, "result")))
				check("rows", offlineTables(t, conn), str(get(step, "rows")))
				check("schema", offlineSchema(t, conn), str(get(step, "schema")))
				check("owners", owners, str(get(step, "owners")))
				calls := float64(0)
				if port.initialized {
					calls = 1
				}
				if calls != get(step, "factoryCalls") {
					t.Fatalf("step %d lazy constructor count %v want %v", i, calls, get(step, "factoryCalls"))
				}
				must(t, conn.QueryRowContext(context.Background(), "SELECT value FROM recovery_gate").Scan(&gateRaw))
				gate = object(parse(t, gateRaw))
				check("quarantined unchanged ACK", obj("head", get(gate, "head"), "minimumReader", get(gate, "minimumReader"), "state", get(gate, "state")), stringify(t, get(step, "gate")))
				check("no new authority intent", offlineRows(t, f.external, "SELECT sequence,previous_hash,payload,hash FROM revocations ORDER BY sequence"), str(get(step, "authority")))
			}
			if get(scenario, "name") == "older-backup-source-marker-exact-qualification" {
				rows := stringify(t, offlineTables(t, conn))
				if strings.Contains(rows, "private-completion") {
					t.Fatal("private retained material resurfaced")
				}
				if rows != get(scenario, "liveRows") {
					t.Fatal("restored qualification not exact live postimage")
				}
			}
		})
	}
}
func TestOfflineOracleSourceDigests(t *testing.T) {
	for _, source := range object(get(loadOfflineOracle(t), "sources")).Members() {
		raw, e := os.ReadFile(filepath.Join("../../..", source.Key))
		must(t, e)
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != source.Value {
			t.Fatalf("offline oracle source changed: %s", source.Key)
		}
	}
}
