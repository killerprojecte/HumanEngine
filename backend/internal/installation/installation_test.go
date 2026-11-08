package installation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/storage"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T) (*sql.DB, *recovery.Authority, string) {
	t.Helper()
	if e := storage.RequireFTS5(); e != nil {
		t.Skip(e)
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "data"), 0700)
	path := filepath.Join(root, "data", "main.sqlite")
	db, e := sql.Open("sqlite3", path)
	if e != nil {
		t.Fatal(e)
	}
	a, e := recovery.OpenInstallationGate(context.Background(), db, path, filepath.Join(root, "authority"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close(); db.Close() })
	return db, a, root
}
func events(t *testing.T, root string) []string {
	t.Helper()
	db, e := storage.OpenExisting(filepath.Join(root, "authority", "authority.sqlite"), false)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	rows, e := db.Query("SELECT payload FROM revocations ORDER BY sequence")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			t.Fatal(e)
		}
		var event map[string]any
		if e = json.Unmarshal([]byte(s), &event); e != nil {
			t.Fatal(e)
		}
		id, _ := event["operationId"].(string)
		out = append(out, strings.Replace(s, id, "<uuid>", 1))
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return out
}

type oracleCase struct {
	Events []string                    `json:"events"`
	Rows   map[string][]map[string]any `json:"rows"`
	Schema map[string][]map[string]any `json:"schema"`
}

func oracle(t *testing.T) map[string]oracleCase {
	t.Helper()
	b, e := os.ReadFile("testdata/oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]oracleCase
	if e = json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestAutomaticBaselineEmptyNegativeAndTransition(t *testing.T) {
	expected := oracle(t)
	for _, scenario := range []string{"fresh", "negative", "transition"} {
		t.Run(scenario, func(t *testing.T) {
			db, a, root := fixture(t)
			ctx := context.Background()
			if scenario != "fresh" {
				if _, e := db.Exec(`CREATE TABLE memories(id TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO memories VALUES('z','{"id":"z","status":"active","lineageStatus":null,"learnedAt":"2026-11-01T00:00:00.000Z","content":"synthetic"}'),('b','{"id":"b","status":"active","lineageStatus":"quarantined","learnedAt":"2026-11-01T00:00:00.000Z","content":"synthetic"}'),('a','{"id":"a","status":"invalidated","lineageStatus":"verified","learnedAt":"2026-11-01T00:00:00.000Z","content":"synthetic"}')`); e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "transition" {
				var event struct {
					Changes []recovery.EligibilityChange `json:"changes"`
				}
				json.Unmarshal([]byte(expected[scenario].Events[0]), &event)
				_, e := a.Write(ctx, db, []recovery.Revocation{recovery.AssertionEligibility{OperationID: "12345678-1234-4123-8123-123456789012", Mode: "transition", Changes: event.Changes}}, 11, func(*recovery.MainMutation) error { return nil })
				if e != nil {
					t.Fatal(e)
				}
			}
			for i := 0; i < 2; i++ {
				if e := EnsureBaseline(ctx, db, a); e != nil {
					t.Fatal(e)
				}
			}
			if got := events(t, root); !reflect.DeepEqual(got, expected[scenario].Events) {
				t.Fatalf("source payload mismatch\ngot %v\nwant %v", got, expected[scenario].Events)
			}
			g, e := recovery.ReadGate(ctx, db)
			if e != nil || g.MinimumReader != 11 {
				t.Fatal(g, e)
			}
		})
	}
}
func TestBaselineACKFailureLeavesDurableGap(t *testing.T) {
	db, a, root := fixture(t)
	db.Exec(`CREATE TRIGGER fail_ack BEFORE UPDATE ON recovery_gate BEGIN SELECT RAISE(ABORT,'synthetic ACK failure'); END`)
	if e := EnsureBaseline(context.Background(), db, a); e == nil {
		t.Fatal("ACK succeeded")
	}
	if len(events(t, root)) != 1 {
		t.Fatal("intent lost")
	}
	var seq int
	db.QueryRow("SELECT json_extract(value,'$.head.sequence') FROM recovery_gate").Scan(&seq)
	if seq != 0 {
		t.Fatal("failed ACK persisted")
	}
	db.Exec("DROP TRIGGER fail_ack")
	if e := EnsureBaseline(context.Background(), db, a); e == nil {
		t.Fatal("gap silently repaired")
	}
}
func TestBaselineReadonlyAndMalformedCensus(t *testing.T) {
	db, a, root := fixture(t)
	ro, e := storage.OpenExisting(filepath.Join(root, "data", "main.sqlite"), false)
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	if e = EnsureBaseline(context.Background(), ro, a); e == nil {
		t.Fatal("readonly baseline")
	}
	if len(events(t, root)) != 0 {
		t.Fatal("readonly append")
	}
	db.Exec(`CREATE TABLE memories(id TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO memories VALUES('bad','{"id":"bad","status":"invalidated","lineageStatus":"unknown"}')`)
	if e = EnsureBaseline(context.Background(), db, a); e == nil {
		t.Fatal("invalid typed event")
	}
	if len(events(t, root)) != 0 {
		t.Fatal("invalid event durable")
	}
}
func TestOpenLateVectorFailureRetainsEnrollment(t *testing.T) {
	if e := storage.RequireFTS5(); e != nil {
		t.Skip(e)
	}
	root := t.TempDir()
	o := Options{Database: filepath.Join(root, "data", "main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"), SourceTimezone: "UTC", VectorExtension: filepath.Join(root, "missing.so")}
	if s, e := Open(context.Background(), o); e == nil {
		s.Close()
		t.Fatal("missing extension accepted")
	}
	db, e := storage.OpenExisting(o.Database, true)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	g, e := recovery.ReadGate(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	if g.Head.Sequence != 0 {
		t.Fatal("baseline after failure")
	}
	if _, e = os.Stat(filepath.Join(root, "authority", "authority.sqlite")); e != nil {
		t.Fatal("authority lost", e)
	}
}
func TestOpenRealVectorFirstStartRestart(t *testing.T) {
	if e := storage.RequireFTS5(); e != nil {
		t.Skip(e)
	}
	vector := os.Getenv("HUMANENGINE_TEST_SQLITE_VEC")
	if vector == "" {
		vector = "../../../node_modules/sqlite-vec-linux-x64/vec0.so"
	}
	vector, _ = filepath.Abs(vector)
	if _, e := os.Stat(vector); e != nil {
		t.Skip("genuine sqlite-vec extension required", e)
	}
	root := t.TempDir()
	o := Options{Database: filepath.Join(root, "data", "main.sqlite"), AuthorityDirectory: filepath.Join(root, "authority"), SourceTimezone: "UTC", VectorExtension: vector}
	for i := 0; i < 2; i++ {
		s, e := Open(context.Background(), o)
		if e != nil {
			t.Fatal(e)
		}
		if s.VectorVersion == "" {
			t.Fatal("missing version")
		}
		if _, e = s.DB.Exec("CREATE VIRTUAL TABLE IF NOT EXISTS installation_vector_probe USING vec0(embedding float[3])"); e != nil {
			t.Fatal(e)
		}
		old, e := s.DB.Conn(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = old.ExecContext(context.Background(), "CREATE TEMP TABLE old_connection_marker(value TEXT)"); e != nil {
			t.Fatal(e)
		}
		if _, e = old.ExecContext(context.Background(), "INSERT INTO installation_vector_probe(rowid,embedding) VALUES(?,?)", i+1, "[1,2,3]"); e != nil {
			t.Fatal(e)
		}
		var oldNative any
		e = old.Raw(func(raw any) error { oldNative = raw; return driver.ErrBadConn })
		if !errors.Is(e, driver.ErrBadConn) {
			t.Fatal(e)
		}
		if e = old.Close(); e != nil && !errors.Is(e, sql.ErrConnDone) {
			t.Fatal(e)
		}
		replacement, e := s.DB.Conn(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if e = replacement.Raw(func(raw any) error {
			if raw == oldNative {
				return errors.New("physical connection reused")
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		var mode, version, bucket string
		var fk, markers, dimensions int
		for query, dest := range map[string]any{"PRAGMA journal_mode": &mode, "PRAGMA foreign_keys": &fk, "SELECT count(*) FROM sqlite_temp_master WHERE name='old_connection_marker'": &markers} {
			if e = replacement.QueryRowContext(context.Background(), query).Scan(dest); e != nil {
				t.Fatal(e)
			}
		}
		if mode != "wal" || fk != 1 || markers != 0 {
			t.Fatal(mode, fk, markers)
		}
		if e = replacement.QueryRowContext(context.Background(), "SELECT vec_version(),usage_bucket('2026-11-01T03:13:02.123Z','+05:45','hour')").Scan(&version, &bucket); e != nil {
			t.Fatal(e)
		}
		if version != s.VectorVersion || bucket != "2026-11-01T02:15:00.000Z" {
			t.Fatal(version, bucket)
		}
		if e = replacement.QueryRowContext(context.Background(), "SELECT vec_length(embedding) FROM installation_vector_probe WHERE rowid=?", i+1).Scan(&dimensions); e != nil || dimensions != 3 {
			t.Fatal(dimensions, e)
		}
		if _, e = replacement.ExecContext(context.Background(), "UPDATE installation_vector_probe SET embedding=? WHERE rowid=?", "[3,2,1]", i+1); e != nil {
			t.Fatal(e)
		}
		if e = replacement.Close(); e != nil {
			t.Fatal(e)
		}
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
	}
	if len(events(t, root)) != 1 {
		t.Fatal("restart baseline duplicated")
	}
	if _, e := os.Stat(filepath.Join(root, "data", ".secrets.key")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("eager key", e)
	}
}
func queryObjects(t *testing.T, db *sql.DB, query string) []map[string]any {
	t.Helper()
	rows, e := db.Query(query)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		t.Fatal(e)
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		pointers := make([]any, len(cols))
		for i := range values {
			pointers[i] = &values[i]
		}
		if e = rows.Scan(pointers...); e != nil {
			t.Fatal(e)
		}
		o := map[string]any{}
		for i, c := range cols {
			switch v := values[i].(type) {
			case int64:
				o[c] = float64(v)
			case []byte:
				o[c] = string(v)
			default:
				o[c] = v
			}
		}
		out = append(out, o)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestSourceSchemaAndUsageMigrationDifferential(t *testing.T) {
	expected := oracle(t)
	for _, scenario := range []string{"fresh", "legacy", "reporting"} {
		t.Run(scenario, func(t *testing.T) {
			db, _, _ := fixture(t)
			ctx := context.Background()
			if scenario != "fresh" {
				if _, e := db.Exec(`CREATE TABLE model_usage (day TEXT NOT NULL, model_id TEXT NOT NULL, provider_id TEXT NOT NULL, api_model TEXT NOT NULL, role TEXT NOT NULL, calls INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, duration_ms REAL NOT NULL, priced_calls INTEGER NOT NULL, estimated_usd REAL NOT NULL, PRIMARY KEY(day,model_id,provider_id,api_model,role)); INSERT INTO model_usage VALUES('2026-11-01','m','p','api','replyer',2,100,20,50,2,0.02);`); e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "reporting" {
				for _, col := range usageColumns[len(reportedColumns):] {
					typ := "INTEGER"
					if strings.HasSuffix(col, "_usd") {
						typ = "REAL"
					}
					if _, e := db.Exec("ALTER TABLE model_usage ADD COLUMN " + col + " " + typ + " NOT NULL DEFAULT 0"); e != nil {
						t.Fatal(e)
					}
				}
				if _, e := db.Exec(`UPDATE model_usage SET cache_read_known_calls=2,cache_read_input_tokens=25;CREATE TABLE model_usage_history AS SELECT * FROM model_usage;CREATE UNIQUE INDEX model_usage_history_key ON model_usage_history(day,model_id,provider_id,api_model,role);CREATE TABLE model_usage_events AS SELECT *, '2026-11-01T12:00:00.000Z' AS at FROM model_usage;CREATE INDEX model_usage_events_at ON model_usage_events(at);UPDATE model_usage SET calls=4,input_tokens=200,output_tokens=40,cache_read_known_calls=4,cache_read_input_tokens=50,priced_calls=4;`); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := db.Exec("CREATE TABLE IF NOT EXISTS secrets (namespace TEXT NOT NULL,name TEXT NOT NULL,ciphertext TEXT NOT NULL,PRIMARY KEY(namespace,name))"); e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 2; i++ {
				if e := InitializeStoreSchema(ctx, db); e != nil {
					t.Fatal(e)
				}
			}
			for table, want := range expected[scenario].Rows {
				if got := queryObjects(t, db, "SELECT * FROM "+table); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s rows got %#v want %#v", table, got, want)
				}
			}
			for table, want := range expected[scenario].Schema {
				if got := queryObjects(t, db, "PRAGMA table_info("+table+")"); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s schema got %#v want %#v", table, got, want)
				}
			}
		})
	}
}
func TestPartialSourceSchemaAndUsageTransactionFailure(t *testing.T) {
	db, _, _ := fixture(t)
	db.Exec("CREATE TABLE source_event_heads(scene_id TEXT,message_id TEXT)")
	if e := InitializeStoreSchema(context.Background(), db); e != nil {
		t.Fatal(e)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='idx_source_event_heads_scene_message'").Scan(&n)
	if n != 0 {
		t.Fatal("partial schema changed")
	}
	db.Exec("CREATE TABLE source_event_revisions(id TEXT);CREATE TABLE source_event_edit_keys(id TEXT);CREATE TABLE source_event_tombstones(id TEXT)")
	if e := InitializeStoreSchema(context.Background(), db); e != nil {
		t.Fatal(e)
	}
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='idx_source_event_heads_scene_message'").Scan(&n)
	if n != 1 {
		t.Fatal("complete schema not indexed")
	}
}

// Rebuilt recovery-era regression: the first migration transaction must roll
// back its ADD COLUMN operations as well as aggregate updates on failure.
func TestUsageMigrationFailureRollsBackColumnsAndValues(t *testing.T) {
	db, _, _ := fixture(t)
	ctx := context.Background()
	if _, e := db.Exec(initialSchema + `INSERT INTO model_usage VALUES('2026-11-01','m','p','api','replyer',2,100,20,50,2,0.02); CREATE TRIGGER reject_usage BEFORE UPDATE ON model_usage BEGIN SELECT RAISE(ABORT,'synthetic usage migration failure'); END;`); e != nil {
		t.Fatal(e)
	}
	if e := InitializeStoreSchema(ctx, db); e == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	for _, row := range queryObjects(t, db, "PRAGMA table_info(model_usage)") {
		if row["name"] == "legacy_calls" {
			t.Fatal("transactional ALTER escaped rollback")
		}
	}
	var calls, tables int
	var estimate float64
	if e := db.QueryRow("SELECT calls,estimated_usd FROM model_usage").Scan(&calls, &estimate); e != nil || calls != 2 || estimate != 0.02 {
		t.Fatal(calls, estimate, e)
	}
	if e := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('model_usage_events','model_usage_history')").Scan(&tables); e != nil || tables != 0 {
		t.Fatal("later migrations ran", tables, e)
	}
	if _, e := db.Exec("DROP TRIGGER reject_usage"); e != nil {
		t.Fatal(e)
	}
	if e := InitializeStoreSchema(ctx, db); e != nil {
		t.Fatal(e)
	}
}
