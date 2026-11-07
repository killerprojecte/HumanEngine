package workingset

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/mattn/go-sqlite3"
)

type offlineFixture struct {
	live   *fixture
	db     *sql.DB
	conn   *sql.Conn
	owner  *recovery.Maintenance
	gate   recovery.Gate
	ledger *sql.DB
}

func backupWorkingSet(t *testing.T, source, target *sql.DB) {
	t.Helper()
	ctx := context.Background()
	src, e := source.Conn(ctx)
	must(t, e)
	defer src.Close()
	dst, e := target.Conn(ctx)
	must(t, e)
	defer dst.Close()
	must(t, dst.Raw(func(to any) error {
		return src.Raw(func(from any) error {
			backup, e := to.(*sqlite3.SQLiteConn).Backup("main", from.(*sqlite3.SQLiteConn), "main")
			if e != nil {
				return e
			}
			done, e := backup.Step(-1)
			e = errors.Join(e, backup.Finish())
			if e != nil {
				return e
			}
			if !done {
				return errors.New("fixture SQLite backup incomplete")
			}
			return nil
		})
	}))
}
func offlineSetup(t *testing.T, seed func(*fixture), events []string) *offlineFixture {
	t.Helper()
	ctx := context.Background()
	live := setup(t)
	if seed != nil {
		seed(live)
	}
	var gateRaw string
	must(t, live.db.QueryRow("SELECT value FROM recovery_gate WHERE id=1").Scan(&gateRaw))
	var gate recovery.Gate
	must(t, json.Unmarshal([]byte(gateRaw), &gate))
	root := t.TempDir()
	path := filepath.Join(root, "staging.sqlite")
	db, e := sql.Open("sqlite3", path)
	must(t, e)
	db.SetMaxOpenConns(1)
	backupWorkingSet(t, live.db, db)
	for _, raw := range events {
		must(t, recovery.ValidateEvent(raw))
		v, e := jsjson.Parse(raw)
		must(t, e)
		var event recovery.Revocation
		switch stringField(v, "kind") {
		case "working_set_pin":
			praw, e := jsjson.Stringify(field(v, "policy"))
			must(t, e)
			p, e := Decode[PinPolicy](praw)
			must(t, e)
			event = recovery.WorkingSetPin{Policy: p}
		case "assertion":
			event = recovery.Assertion{ID: stringField(v, "id"), ThroughVersion: int64(numberField(v, "throughVersion", 0)), Terminal: boolField(v, "terminal")}
		case "commitment_delete":
			event = recovery.CommitmentDelete{SceneID: stringField(v, "sceneId"), ID: stringField(v, "id"), DeletionEpoch: numberField(v, "deletionEpoch", 0)}
		default:
			t.Fatal("fixture unsupported event", raw)
		}
		must(t, live.authority.WithWriteSession(ctx, live.db, func(s *recovery.WriteSession) error {
			if _, ok := event.(recovery.WorkingSetPin); ok {
				if e := s.RequireReader(4); e != nil {
					return e
				}
			}
			_, e := s.AppendIntent(event)
			return e
		}))
	}
	gate.State = "quarantine"
	raw, e := json.Marshal(gate)
	must(t, e)
	_, e = db.Exec("UPDATE recovery_gate SET value=? WHERE id=1", string(raw))
	must(t, e)
	conn, e := db.Conn(ctx)
	must(t, e)
	owner, e := recovery.AdmitMaintenance(ctx, conn, path, live.authority)
	must(t, e)
	ledger, e := sql.Open("sqlite3", filepath.Join(gate.Authority, "authority.sqlite")+"?mode=ro")
	must(t, e)
	t.Cleanup(func() { _ = conn.Close(); _ = db.Close(); _ = ledger.Close() })
	return &offlineFixture{live, db, conn, owner, gate, ledger}
}
func offlineSQLRows(t *testing.T, conn *sql.Conn, q string) []any {
	t.Helper()
	rows, e := conn.QueryContext(context.Background(), q)
	must(t, e)
	defer rows.Close()
	columns, e := rows.Columns()
	must(t, e)
	out := []any{}
	for rows.Next() {
		vals := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		must(t, rows.Scan(ptrs...))
		v := new(jsjson.Object)
		for i, x := range vals {
			if n, ok := x.(int64); ok {
				x = float64(n)
			}
			must(t, v.Set(columns[i], x))
		}
		out = append(out, v)
	}
	must(t, rows.Err())
	return out
}
func offlineInspect(t *testing.T, conn *sql.Conn) (*jsjson.Object, []any) {
	tables := new(jsjson.Object)
	for _, row := range offlineSQLRows(t, conn, "SELECT name FROM sqlite_master WHERE type='table' AND name GLOB 'memory_working_set_*' ORDER BY name") {
		name := stringField(row, "name")
		must(t, tables.Set(name, offlineSQLRows(t, conn, `SELECT * FROM "`+name+`" ORDER BY rowid`)))
	}
	return tables, offlineSQLRows(t, conn, "SELECT type,name,sql FROM sqlite_master WHERE name GLOB 'memory_working_set_*' AND sql IS NOT NULL ORDER BY rowid")
}
func TestActualSourceOfflineBackupTraces(t *testing.T) {
	raw, e := os.ReadFile("testdata/offline-source-oracle.json")
	must(t, e)
	root, e := jsjson.ParseBytes(raw)
	must(t, e)
	for _, scenario := range array(field(root, "scenarios")) {
		t.Run(stringField(scenario, "name"), func(t *testing.T) {
			events := typed[[]string](t, field(scenario, "events"))
			f := offlineSetup(t, func(live *fixture) {
				for _, row := range array(field(scenario, "initialSchema")) {
					_, e := live.db.Exec(stringField(row, "sql"))
					must(t, e)
				}
				for _, row := range array(field(scenario, "initialUnrelatedSchema")) {
					_, e := live.db.Exec(stringField(row, "sql"))
					must(t, e)
				}
				for _, table := range object(field(scenario, "initial")).Members() {
					for _, row := range array(table.Value) {
						columns, marks := []string{}, []string{}
						args := []any{}
						for _, m := range object(row).Members() {
							columns = append(columns, `"`+m.Key+`"`)
							marks = append(marks, "?")
							args = append(args, m.Value)
						}
						_, e := live.db.Exec(`INSERT INTO "`+table.Key+`" (`+strings.Join(columns, ",")+") VALUES("+strings.Join(marks, ",")+")", bind(args)...)
						must(t, e)
					}
				}
			}, events)
			config := field(scenario, "config")
			calls := []PinPolicy{}
			ports := OfflinePorts{}
			if !boolField(config, "noResolver") {
				ports.ResolvePinTarget = func(_ context.Context, _ *recovery.MaintenanceTx, p PinPolicy) (string, error) {
					calls = append(calls, p)
					target := stringField(config, "target")
					if target == "throw" {
						return "", errors.New("synthetic_target_failure")
					}
					if target == "" {
						target = "current"
					}
					return target, nil
				}
			}
			owner, e := NewOfflineMaintenance(f.owner, ports)
			must(t, e)
			ops := array(field(config, "ops"))
			if field(config, "ops") == nil {
				ops = []any{obj("kind", "assert"), obj("kind", "reset"), obj("kind", "verify"), obj("kind", "verify")}
			}
			trace := array(field(scenario, "trace"))
			if len(ops) != len(trace) {
				t.Fatal("trace count")
			}
			for i, op := range ops {
				calls = []PinPolicy{}
				var result any
				undefined := true
				act := func(b *OfflineMaintenance) error {
					if q := stringField(op, "sql"); q != "" {
						if b.tx != nil {
							_, e := b.tx.ExecContext(context.Background(), q)
							if e != nil {
								return e
							}
						} else {
							_, e := f.conn.ExecContext(context.Background(), q)
							if e != nil {
								return e
							}
						}
					}
					switch stringField(op, "kind") {
					case "assert":
						undefined = false
						v, e := b.AssertSupported(context.Background())
						result = v
						return e
					case "reset":
						if e := b.ResetInScope(context.Background()); e != nil {
							return e
						}
					case "verify":
						if e := b.VerifyPinsInScope(context.Background(), events); e != nil {
							return e
						}
					case "resetVerify":
						if e := b.ResetInScope(context.Background()); e != nil {
							return e
						}
						if e := b.VerifyPinsInScope(context.Background(), events); e != nil {
							return e
						}
					default:
						t.Fatal("unknown op", op)
					}
					if boolField(op, "rollback") {
						return errors.New("synthetic_outer_rollback")
					}
					return nil
				}
				if boolField(op, "outside") {
					e = act(owner)
				} else {
					e = f.owner.Apply(context.Background(), 1, func(tx *recovery.MaintenanceTx) error { return act(owner.WithScope(context.Background(), tx)) })
				}
				expected := trace[i]
				label := stringField(op, "kind")
				if want := stringField(expected, "error"); want != "" {
					if e == nil || e.Error() != want {
						t.Fatalf("%d %s error %v want %s", i, label, e, want)
					}
				} else {
					must(t, e)
					if undefined != boolField(expected, "undefined") {
						t.Fatal("undefined mismatch")
					}
					if !undefined {
						got, _ := JSON(result)
						want, _ := jsjson.Stringify(field(expected, "result"))
						compareJSON(t, label, got, want)
					}
				}
				actualCalls, e := JSON(calls)
				must(t, e)
				expectedCalls, e := jsjson.Stringify(field(expected, "calls"))
				must(t, e)
				compareJSON(t, label+" calls", actualCalls, expectedCalls)
				tables, schema := offlineInspect(t, f.conn)
				got, e := jsjson.Stringify(tables)
				must(t, e)
				want, e := jsjson.Stringify(field(expected, "tables"))
				must(t, e)
				compareJSON(t, label+" tables", got, want)
				got, e = jsjson.Stringify(schema)
				must(t, e)
				want, e = jsjson.Stringify(field(expected, "schema"))
				must(t, e)
				compareJSON(t, label+" schema", got, want)
			}
			unrelated := []any{}
			for _, row := range array(field(scenario, "initialUnrelatedSchema")) {
				var typ, name, definition string
				must(t, f.conn.QueryRowContext(context.Background(), "SELECT type,name,sql FROM sqlite_master WHERE name=?", stringField(row, "name")).Scan(&typ, &name, &definition))
				unrelated = append(unrelated, obj("type", typ, "name", name, "sql", definition))
			}
			gotUnrelated, e := jsjson.Stringify(unrelated)
			must(t, e)
			wantUnrelated, e := jsjson.Stringify(field(scenario, "initialUnrelatedSchema"))
			must(t, e)
			compareJSON(t, "unrelated schema survives actual backup and maintenance", gotUnrelated, wantUnrelated)
			var final string
			must(t, f.conn.QueryRowContext(context.Background(), "SELECT value FROM recovery_gate WHERE id=1").Scan(&final))
			var gate recovery.Gate
			must(t, json.Unmarshal([]byte(final), &gate))
			if gate != f.gate {
				t.Fatal("offline changed gate", gate, f.gate)
			}
			rows, e := f.ledger.Query("SELECT payload FROM revocations ORDER BY sequence")
			must(t, e)
			actual := []string{}
			for rows.Next() {
				var raw string
				must(t, rows.Scan(&raw))
				actual = append(actual, raw)
			}
			must(t, rows.Err())
			must(t, rows.Close())
			got, e := JSON(actual)
			must(t, e)
			want, e := JSON(events)
			must(t, e)
			compareJSON(t, "authority unchanged exact raw events", got, want)
		})
	}
}
