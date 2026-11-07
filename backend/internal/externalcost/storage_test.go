package externalcost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

type fixture struct {
	Raw     string
	Sources map[string]string
	Cases   map[string]struct {
		Events []string
		Error  string
		Heads  []json.RawMessage
		Rows   []struct {
			Payload     string
			PayloadHash string `json:"payload_hash"`
		}
	}
	Storage struct {
		Row struct {
			Payload     string
			PayloadHash string `json:"payload_hash"`
		}
		Hash, Material, Schema string
	}
}

func load(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/source.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for path, hash := range f.Sources {
		b, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != hash {
			t.Fatalf("source fixture stale: %s", path)
		}
	}
	return f
}
func database(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "stage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}
func receipt(t *testing.T, raw string) Receipt {
	t.Helper()
	r, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestSourceFixtureLive(t *testing.T) {
	load(t)
	cmd := exec.Command("node", "--import", "tsx", "backend/internal/externalcost/testdata/source.mjs")
	cmd.Dir = "../../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Node fixture: %v %s", err, out)
	}
	saved, err := os.ReadFile("testdata/source.json")
	must(t, err)
	if strings.TrimSpace(string(out)) != strings.TrimSpace(string(saved)) {
		t.Fatal("actual Node source fixture changed")
	}
}
func TestStorageMaterialAndCanonicalOrder(t *testing.T) {
	f := load(t)
	db := database(t)
	s := Storage{db}
	r := receipt(t, f.Raw)
	if r.JSON() != f.Storage.Row.Payload || r.Hash() != f.Storage.Row.PayloadHash {
		t.Fatal("Zod schema order/hash mismatch")
	}
	must(t, s.Put(ctx, r))
	must(t, s.Put(ctx, r))
	got, err := s.Latest(ctx, "cost")
	must(t, err)
	if got.JSON() != r.JSON() {
		t.Fatal("latest mismatch")
	}
	absent, err := s.Correction(ctx, "none")
	must(t, err)
	if absent != nil {
		t.Fatal("unexpected correction")
	}
	_, err = db.Exec("UPDATE "+Table+" SET payload=?", f.Raw)
	must(t, err)
	got, err = s.Latest(ctx, "cost")
	must(t, err)
	if got.JSON() != r.JSON() {
		t.Fatal("reordered persisted material mismatch")
	}
	for _, test := range []struct{ name, sql, want string }{{"hash", "UPDATE " + Table + " SET payload_hash='bad'", f.Storage.Hash}, {"material", "UPDATE " + Table + " SET recorded_at='bad'", f.Storage.Material}, {"schema", "ALTER TABLE " + Table + " ADD COLUMN surprise TEXT", f.Storage.Schema}} {
		t.Run(test.name, func(t *testing.T) {
			d := database(t)
			st := Storage{d}
			must(t, st.Put(ctx, r))
			_, err := d.Exec(test.sql)
			must(t, err)
			_, err = st.Current(ctx)
			if err == nil || err.Error() != test.want {
				t.Fatalf("got %v want %s", err, test.want)
			}
		})
	}
}

// Test maintenance capability is already admitted by the fixture owner. It
// models source transaction and gate mutation boundaries; it is deliberately
// not an exported substitute for independent authority/path admission.
type maintenance struct{ db *sql.DB }

func (m maintenance) ReadStorage() Storage { return Storage{m.db} }
func (m maintenance) ApplyReceipt(ctx context.Context, fn func(Storage) error) (err error) {
	c, err := m.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	var state string
	var reader int
	if err = c.QueryRowContext(ctx, "SELECT json_extract(value,'$.state'),json_extract(value,'$.minimumReader') FROM recovery_gate WHERE id=1").Scan(&state, &reader); err != nil {
		return err
	}
	if state != "quarantine" || reader > 11 {
		return fmt.Errorf("recovery_maintenance_denied")
	}
	if reader < 10 {
		if _, err = c.ExecContext(ctx, "UPDATE recovery_gate SET value=json_set(value,'$.minimumReader',10) WHERE id=1"); err != nil {
			return err
		}
	}
	if err = fn(Storage{c}); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	return err
}

const gate = `{"head":{"instanceId":"00000000-0000-0000-0000-000000000000","sequence":0,"hash":"0000000000000000000000000000000000000000000000000000000000000000"},"authority":"/synthetic-independent-authority","minimumReader":3,"state":"quarantine"}`

func stage(t *testing.T) (*sql.DB, maintenance) {
	db := database(t)
	_, err := db.Exec("CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO recovery_gate VALUES(1,?)", gate)
	must(t, err)
	return db, maintenance{db}
}
func TestSourceReplayDiskQuarantineAndPartialCommit(t *testing.T) {
	f := load(t)
	for name, c := range f.Cases {
		t.Run(name, func(t *testing.T) {
			db, m := stage(t)
			err := ReplayOffline(ctx, m, c.Events)
			if c.Error == "" {
				must(t, err)
			} else if err == nil {
				t.Fatal("expected source failure")
			} else if !strings.Contains(c.Error, "UNIQUE") && err.Error() != c.Error {
				t.Fatalf("got %v want %s", err, c.Error)
			} else if strings.Contains(c.Error, "UNIQUE") && !strings.Contains(err.Error(), "UNIQUE") {
				t.Fatal(err)
			}
			rows, err := db.Query("SELECT payload,payload_hash FROM " + Table + " ORDER BY revision")
			must(t, err)
			i := 0
			for rows.Next() {
				var payload, hash string
				must(t, rows.Scan(&payload, &hash))
				if i >= len(c.Rows) || payload != c.Rows[i].Payload || hash != c.Rows[i].PayloadHash {
					t.Fatal("source row mismatch")
				}
				i++
			}
			must(t, rows.Err())
			rows.Close()
			if i != len(c.Rows) {
				t.Fatalf("retained %d rows want %d", i, len(c.Rows))
			}
			var state, head, authority string
			var minimum int
			must(t, db.QueryRow("SELECT json_extract(value,'$.state'),json_extract(value,'$.minimumReader'),json_extract(value,'$.head'),json_extract(value,'$.authority') FROM recovery_gate").Scan(&state, &minimum, &head, &authority))
			var g map[string]json.RawMessage
			must(t, json.Unmarshal([]byte(gate), &g))
			if state != "quarantine" || minimum != 10 || head != string(g["head"]) || authority != "/synthetic-independent-authority" {
				t.Fatal("replay changed global gate/head or failed reader upgrade")
			}
		})
	}
}
func TestExactHeadComparisonAndCallerAdmission(t *testing.T) {
	f := load(t)
	db, m := stage(t)
	r := receipt(t, f.Raw)
	must(t, m.ReadStorage().Put(ctx, r))
	if err := ReplayOffline(ctx, m, nil); err == nil || err.Error() != "recovery_external_cost_head_mismatch" {
		t.Fatal(err)
	}
	_, err := db.Exec("UPDATE recovery_gate SET value=json_set(value,'$.state','ready')")
	must(t, err)
	err = ReplayOffline(ctx, m, f.Cases["success"].Events)
	if err == nil || err.Error() != "recovery_maintenance_denied" {
		t.Fatal(err)
	}
}
func TestStorageSchemaAndValidationBoundary(t *testing.T) {
	f := load(t)
	for _, sqlText := range []string{"CREATE VIEW budget_external_cost_other AS SELECT 1", "CREATE TABLE budget_external_cost_revisions(payload TEXT)", "CREATE TABLE budget_external_cost_revisions(id,revision,payload,payload_hash,correction_id,recorded_at); CREATE TABLE budget_external_cost_extra(x)"} {
		db := database(t)
		_, err := db.Exec(sqlText)
		must(t, err)
		_, err = (Storage{db}).AssertSupported(ctx)
		if err == nil || err.Error() != "external_cost_schema_unsupported" {
			t.Fatal(err)
		}
	}
	// Storage accepts revision/correction pairs rejected by recovery refinement.
	for _, raw := range []string{strings.Replace(f.Raw, `"correctionId":null`, `"correctionId":"initial"`, 1), strings.Replace(f.Raw, `"revision":1`, `"revision":2`, 1)} {
		r := receipt(t, raw)
		must(t, (Storage{database(t)}).Put(ctx, r))
	}
	for _, raw := range []string{strings.Replace(f.Raw, "9007199254740991", "9007199254740992", 1), strings.Replace(f.Raw, `"revision":1`, `"revision":1001`, 1), strings.Replace(f.Raw, `"schemaVersion":1`, `"schemaVersion":1,"extra":true`, 1), strings.Replace(f.Raw, `"correctionId":null`, `"correctionId":"!"`, 1), strings.ReplaceAll(f.Raw, "2026-11-07", "2026-02-30")} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestNegativeZeroConflictAndCapacityOrder(t *testing.T) {
	f := load(t)
	r := receipt(t, strings.Replace(f.Raw, "9007199254740991", "-0", 1))
	s := Storage{database(t)}
	must(t, s.Put(ctx, r))
	if err := s.Put(ctx, r); err == nil || err.Error() != "external_cost_conflict" {
		t.Fatalf("negative zero source strict equality: %v", err)
	}
	db := database(t)
	s = Storage{db}
	must(t, s.Ensure(ctx))
	_, err := db.Exec("WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10001) INSERT INTO " + Table + " SELECT i,1,'malformed','bad',NULL,'bad' FROM n")
	must(t, err)
	_, err = s.Current(ctx)
	if err == nil || err.Error() != "external_cost_capacity" {
		t.Fatal(err)
	}
}

func TestCorrectionLookupConflictAndRollback(t *testing.T) {
	f := load(t)
	db, m := stage(t)
	must(t, ReplayOffline(ctx, m, f.Cases["success"].Events))
	s := m.ReadStorage()
	r, err := s.Correction(ctx, "fix")
	must(t, err)
	if r == nil || r.Revision() != 2 {
		t.Fatal("correction missing")
	}
	changed := receipt(t, strings.Replace(f.Raw, "9007199254740991", "2", 1))
	if err = s.Put(ctx, changed); err == nil || err.Error() != "external_cost_conflict" {
		t.Fatal(err)
	}
	// Failure in the first receipt rolls back the operational reader upgrade too.
	db2, m2 := stage(t)
	must(t, m2.ReadStorage().Ensure(ctx))
	_, err = db2.Exec("CREATE TRIGGER reject_receipt BEFORE INSERT ON " + Table + " BEGIN SELECT RAISE(ABORT,'synthetic_failure'); END")
	must(t, err)
	if err = ReplayOffline(ctx, m2, f.Cases["success"].Events); err == nil {
		t.Fatal("expected failure")
	}
	var minimum, count int
	must(t, db2.QueryRow("SELECT json_extract(value,'$.minimumReader') FROM recovery_gate").Scan(&minimum))
	must(t, db2.QueryRow("SELECT count(*) FROM "+Table).Scan(&count))
	if minimum != 3 || count != 0 {
		t.Fatal("failed receipt leaked transaction writes")
	}
	_ = db
}

// Source current() returns an array even when an existing table has no rows.
func TestCurrentEmptyRetainsArrayShape(t *testing.T) {
	s := Storage{database(t)}
	must(t, s.Ensure(ctx))
	got, err := s.Current(ctx)
	must(t, err)
	if got == nil || len(got) != 0 {
		t.Fatalf("empty current = %#v", got)
	}
}
