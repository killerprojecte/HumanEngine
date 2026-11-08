package memoryservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"os"
	"strings"
	"testing"
)

func offlineMust(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func offlineOracle(t *testing.T) *Object {
	t.Helper()
	raw, e := os.ReadFile("../offlinelifecycle/testdata/source.json")
	offlineMust(t, e)
	v, e := jsjson.Parse(string(raw))
	offlineMust(t, e)
	return object(v)
}
func offlineSQLRows(t *testing.T, c *sql.Conn, q string) []any {
	t.Helper()
	rows, e := c.QueryContext(context.Background(), q)
	offlineMust(t, e)
	defer rows.Close()
	columns, e := rows.Columns()
	offlineMust(t, e)
	out := []any{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		offlineMust(t, rows.Scan(dest...))
		row := new(Object)
		for i, k := range columns {
			v := values[i]
			if n, ok := v.(int64); ok {
				v = float64(n)
			}
			if b, ok := v.([]byte); ok {
				data := []any{}
				for _, x := range b {
					data = append(data, float64(x))
				}
				v = epObj("type", "Buffer", "data", data)
			}
			_ = row.Set(k, v)
		}
		out = append(out, row)
	}
	offlineMust(t, rows.Err())
	return out
}
func offlineSnapshot(t *testing.T, c *sql.Conn, template *Object) *Object {
	out := new(Object)
	for _, m := range template.Members() {
		_ = out.Set(m.Key, offlineSQLRows(t, c, `SELECT * FROM "`+m.Key+`"`))
	}
	return out
}
func offlineSeed(t *testing.T, f scopedFixture, oracle, scenario *Object) {
	t.Helper()
	schemas := object(value(oracle, "schemas"))
	before := object(value(scenario, "before"))
	for _, m := range before.Members() {
		var exists bool
		offlineMust(t, f.main.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name=? AND type='table')", m.Key).Scan(&exists))
		if !exists {
			_, e := f.main.Exec(text(schemas, m.Key))
			offlineMust(t, e)
		}
		_, e := f.main.Exec(`DELETE FROM "` + m.Key + `"`)
		offlineMust(t, e)
	}
	for _, m := range before.Members() {
		for _, r := range epArr(m.Value) {
			row := object(r)
			columns, marks, args := []string{}, []string{}, []any{}
			for _, v := range row.Members() {
				columns = append(columns, `"`+v.Key+`"`)
				marks = append(marks, "?")
				a := v.Value
				if epGet(a, "type") == "Buffer" {
					b := []byte{}
					for _, n := range epArr(epGet(a, "data")) {
						b = append(b, byte(n.(float64)))
					}
					a = b
				}
				args = append(args, a)
			}
			_, e := f.main.Exec(`INSERT INTO "`+m.Key+`" (`+strings.Join(columns, ",")+") VALUES("+strings.Join(marks, ",")+")", args...)
			offlineMust(t, e)
		}
	}
}
func offlineAdmit(t *testing.T, f scopedFixture, minimum float64) (*recovery.Maintenance, *sql.Conn, recovery.Gate) {
	t.Helper()
	ctx := context.Background()
	gate, e := recovery.ReadGate(ctx, f.main)
	offlineMust(t, e)
	gate.State = "quarantine"
	gate.MinimumReader = int64(minimum)
	raw, e := json.Marshal(gate)
	offlineMust(t, e)
	_, e = f.main.Exec("UPDATE recovery_gate SET value=? WHERE id=1", string(raw))
	offlineMust(t, e)
	conn, e := f.main.Conn(ctx)
	offlineMust(t, e)
	t.Cleanup(func() { conn.Close() })
	var path string
	for _, r := range offlineSQLRows(t, conn, "PRAGMA database_list") {
		if epGet(r, "name") == "main" {
			path = epGet(r, "file").(string)
		}
	}
	m, e := recovery.AdmitMaintenance(ctx, conn, path, f.recovery)
	offlineMust(t, e)
	return m, conn, gate
}
func offlineOptions(t *testing.T) OfflineLifecycleOptions {
	a := testOptions(t)
	return OfflineLifecycleOptions{Assertions: assertions.Options{DateParser: a.DateParser, Now: func() string { return "2026-10-28T12:00:00.000Z" }}, Authority: a, Commitments: commitments.Options{DateParser: a.DateParser, Compare: a.Compare, Now: func() string { return "2026-10-28T12:00:00.000Z" }}, Projection: projections.Options{Now: func() float64 { return 1793188800000 }, Token: func() (string, error) { return "offline-projection-token", nil }}, WorkingSet: workingset.Ports{Enabled: func() bool { return false }, Now: func() float64 { return 1793188800000 }, Token: func() (string, error) { return "offline-workspace-token", nil }}, ProjectionEnabled: func() bool { return false }, CommitmentsEnabled: func(commitments.Context) (bool, error) { return true, nil }, Log: func(a, b, c string) { t.Log(a, b, c) }}
}
func TestOfflineLifecycleActualSource(t *testing.T) {
	oracle := offlineOracle(t)
	for _, raw := range epArr(value(oracle, "cases")) {
		scenario := object(raw)
		t.Run(text(scenario, "name"), func(t *testing.T) {
			_, f := serviceSetup(t)
			offlineSeed(t, f, oracle, scenario)
			minimum, _ := epGet(value(scenario, "gateBefore"), "minimumReader").(float64)
			m, c, gate := offlineAdmit(t, f, minimum)
			options := offlineOptions(t)
			settings := object(value(scenario, "settings"))
			options.ProjectionEnabled = func() bool { return value(settings, "projectionEnabled") == true }
			options.WorkingSet.Enabled = func() bool { return value(settings, "workingSetEnabled") == true }
			options.CommitmentsEnabled = func(commitments.Context) (bool, error) { return value(settings, "commitmentsEnabled") == true, nil }
			sequence := int64(0)
			uuid := func() string { sequence++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", sequence) }
			options.Assertions.OperationID = uuid
			options.WorkingSet.Token = func() (string, error) { return uuid(), nil }
			options.Projection.Token = func() (string, error) { return uuid(), nil }
			owner, e := NewOfflineLifecycle(m, options)
			offlineMust(t, e)
			for i, rawStep := range epArr(value(scenario, "steps")) {
				step := object(rawStep)
				if n, ok := value(step, "uuidSequenceBefore").(float64); ok {
					sequence = int64(n)
				}
				var effects []string
				switch text(scenario, "operation") {
				case "redactRecoveredHistory":
					effects, e = owner.RedactRecoveredHistory(context.Background(), text(scenario, "id"))
				case "eraseRecoveredAssertion":
					effects, e = owner.EraseRecoveredAssertion(context.Background(), text(scenario, "id"))
				default:
					t.Fatal("unknown operation")
				}
				if expected := text(step, "error"); expected != "" {
					if e == nil || !strings.Contains(e.Error(), expected) {
						t.Fatalf("step %d error %v want %s", i, e, expected)
					}
				} else {
					offlineMust(t, e)
					actual := []any{}
					for _, effect := range effects {
						v, e := jsjson.Parse(effect)
						offlineMust(t, e)
						actual = append(actual, v)
					}
					if !deepEqual(actual, value(step, "effects")) {
						a, _ := jsjson.Stringify(actual)
						b, _ := jsjson.Stringify(value(step, "effects"))
						t.Fatalf("step %d effects\n%s\nwant\n%s", i, a, b)
					}
				}
				expected := object(value(step, "after"))
				actual := offlineSnapshot(t, c, expected)
				if !deepEqual(actual, expected) {
					a, _ := jsjson.Stringify(actual)
					b, _ := jsjson.Stringify(expected)
					t.Fatalf("step %d rows\n%s\nwant\n%s", i, a, b)
				}
				var encoded string
				offlineMust(t, c.QueryRowContext(context.Background(), "SELECT value FROM recovery_gate WHERE id=1").Scan(&encoded))
				var after recovery.Gate
				offlineMust(t, json.Unmarshal([]byte(encoded), &after))
				if after.State != "quarantine" || after.Head != gate.Head {
					t.Fatal("offline lifecycle changed quarantine/head")
				}
				if want := epGet(value(step, "gate"), "minimumReader"); float64(after.MinimumReader) != want {
					t.Fatalf("minimum reader %d want %v", after.MinimumReader, want)
				}
				head, e := f.recovery.Head(context.Background())
				offlineMust(t, e)
				if head != gate.Head {
					t.Fatal("offline lifecycle changed durable authority")
				}
			}
		})
	}
}
func TestOfflineLifecycleRollbackAndTransactionLoss(t *testing.T) {
	for _, kind := range []string{"ABORT", "ROLLBACK"} {
		t.Run(kind, func(t *testing.T) {
			oracle := offlineOracle(t)
			scenario := object(epArr(value(oracle, "cases"))[0])
			_, f := serviceSetup(t)
			offlineSeed(t, f, oracle, scenario)
			_, e := f.main.Exec(fmt.Sprintf(`CREATE TRIGGER offline_failure BEFORE UPDATE ON memory_assertion_revisions BEGIN SELECT RAISE(%s,'offline_failure'); END`, kind))
			offlineMust(t, e)
			m, c, gate := offlineAdmit(t, f, 1)
			owner, e := NewOfflineLifecycle(m, offlineOptions(t))
			offlineMust(t, e)
			before := offlineSnapshot(t, c, object(value(scenario, "before")))
			effects, e := owner.RedactRecoveredHistory(context.Background(), text(scenario, "id"))
			if e == nil || effects != nil {
				t.Fatalf("failed root leaked receipts %v %v", effects, e)
			}
			after := offlineSnapshot(t, c, before)
			if !deepEqual(before, after) {
				t.Fatal("failed root changed rows")
			}
			head, e := f.recovery.Head(context.Background())
			offlineMust(t, e)
			if head != gate.Head {
				t.Fatal("failed offline root changed authority")
			}
		})
	}
}

func TestOfflineLifecycleOwnerScopesExpire(t *testing.T) {
	oracle := offlineOracle(t)
	scenario := object(epArr(value(oracle, "cases"))[0])
	_, f := serviceSetup(t)
	offlineSeed(t, f, oracle, scenario)
	m, _, _ := offlineAdmit(t, f, 1)
	owner, e := NewOfflineLifecycle(m, offlineOptions(t))
	offlineMust(t, e)
	var facade *offlineFacade
	offlineMust(t, m.Apply(context.Background(), 1, func(tx *recovery.MaintenanceTx) error {
		var e error
		facade, e = owner.bind(context.Background(), tx)
		return e
	}))
	if e = facade.d.Assertions.WithMaintenanceScope(func(*recovery.MaintenanceTx) error { t.Fatal("expired assertion scope invoked"); return nil }); e == nil {
		t.Fatal("expired assertions accepted")
	}
	if _, e = facade.d.Knowledge.Storage.Rows(nil, nil); e == nil {
		t.Fatal("expired knowledge accepted")
	}
	if _, e = facade.d.Semantic.Rows(nil); e == nil {
		t.Fatal("expired semantic accepted")
	}
	if _, e = facade.d.Relations.PurgeAssertion(text(scenario, "id")); e == nil {
		t.Fatal("expired relations accepted")
	}
	if e = facade.d.Commitments.WithMaintenanceScope(func(*recovery.MaintenanceTx) error { t.Fatal("expired commitment scope invoked"); return nil }); e == nil {
		t.Fatal("expired commitments accepted")
	}
	if e = facade.d.WorkingSet.Invalidate(context.Background(), "s", []workingset.Entity{{Kind: "assertion", ID: text(scenario, "id")}}); e == nil {
		t.Fatal("expired workspace accepted")
	}
	if e = facade.d.Projections.Remove(text(scenario, "id")); e == nil {
		t.Fatal("expired projections accepted")
	}
}
