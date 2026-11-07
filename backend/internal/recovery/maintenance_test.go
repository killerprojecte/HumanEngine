package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func maintenanceFixture(t *testing.T) (*Maintenance, *sql.Conn, *Authority, *sql.DB, Gate) {
	t.Helper()
	a, _, external, g := fixture(t)
	dir := filepath.Join(filepath.Dir(a.path), "staging")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "main.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL); CREATE TABLE effects(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	g.State, g.MinimumReader = "quarantine", 1
	save(t, db, g)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); db.Close() })
	m, err := AdmitMaintenance(context.Background(), conn, path, a)
	if err != nil {
		t.Fatal(err)
	}
	return m, conn, a, external, g
}
func effect(value string) func(*MaintenanceTx) error {
	return func(tx *MaintenanceTx) error {
		_, e := tx.ExecContext(context.Background(), "INSERT INTO effects VALUES(?)", value)
		return e
	}
}
func TestMaintenanceAtomicReaderAndQuarantine(t *testing.T) {
	m, c, a, _, g := maintenanceFixture(t)
	ctx := context.Background()
	boom := errors.New("receipt failed")
	if e := m.Apply(ctx, 10, func(tx *MaintenanceTx) error {
		if e := effect("rolled back")(tx); e != nil {
			return e
		}
		return boom
	}); !errors.Is(e, boom) {
		t.Fatal(e)
	}
	after, _, e := maintenanceMetadata(ctx, c)
	if e != nil || after != g {
		t.Fatal(after, e)
	}
	var count int
	c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&count)
	if count != 0 {
		t.Fatal(count)
	}
	var retained *MaintenanceTx
	if e = m.Apply(ctx, 10, func(tx *MaintenanceTx) error { retained = tx; return effect("committed")(tx) }); e != nil {
		t.Fatal(e)
	}
	if _, e = retained.ExecContext(ctx, "INSERT INTO effects VALUES('escaped')"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("expired handle", e)
	}
	after, _, e = maintenanceMetadata(ctx, c)
	g.MinimumReader = 10
	if e != nil || after != g {
		t.Fatal(after, e)
	}
	if a.Scans() != 1 {
		t.Fatal("repeated full audit", a.Scans())
	}
}
func TestMaintenanceNestedSavepointsAndCallerRollback(t *testing.T) {
	m, c, _, _, g := maintenanceFixture(t)
	ctx := context.Background()
	boom := errors.New("inner")
	if _, e := c.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	if e := m.Apply(ctx, 10, func(tx *MaintenanceTx) error {
		if e := effect("outer")(tx); e != nil {
			return e
		}
		if e := tx.Apply(ctx, 11, func(inner *MaintenanceTx) error {
			if e := effect("inner")(inner); e != nil {
				return e
			}
			return boom
		}); !errors.Is(e, boom) {
			t.Fatal(e)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	var count int
	c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	after, _, e := maintenanceMetadata(ctx, c)
	if e != nil || after.MinimumReader != 10 {
		t.Fatal(after, e)
	}
	if _, e = c.ExecContext(ctx, "ROLLBACK"); e != nil {
		t.Fatal(e)
	}
	after, _, e = maintenanceMetadata(ctx, c)
	if e != nil || after != g {
		t.Fatal(after, e)
	}
	c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&count)
	if count != 0 {
		t.Fatal(count)
	}
}
func TestMaintenanceCancellationAndAuthorityChange(t *testing.T) {
	m, c, a, external, g := maintenanceFixture(t)
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	e := m.Apply(canceled, 10, func(tx *MaintenanceTx) error {
		if e := effect("cancelled")(tx); e != nil {
			return e
		}
		cancel()
		return nil
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	after, _, e := maintenanceMetadata(ctx, c)
	if e != nil || after != g {
		t.Fatal(after, e)
	}
	payload := `{"kind":"source","sceneId":"s","messageId":"m"}`
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|1|%s|%s", g.Head.InstanceID, g.Head.Hash, payload)))
	if _, e = external.Exec("INSERT INTO revocations VALUES(1,?,?,?)", g.Head.Hash, payload, hex.EncodeToString(sum[:])); e != nil {
		t.Fatal(e)
	}
	if e = m.Apply(ctx, 10, effect("stale")); e == nil || !strings.Contains(e.Error(), "head_changed") {
		t.Fatal(e)
	}
	if a.Scans() != 2 {
		t.Fatal(a.Scans())
	}
	// A fresh independently audited capability may admit the old valid prefix.
	fresh, e := AdmitMaintenance(ctx, c, m.database, a)
	if e != nil {
		t.Fatal(e)
	}
	if e = fresh.Apply(ctx, 10, effect("fresh")); e != nil {
		t.Fatal(e)
	}
	after, _, e = maintenanceMetadata(ctx, c)
	if e != nil || after.Head != g.Head || after.State != "quarantine" {
		t.Fatal(after, e)
	}
}
func TestMaintenanceAdmissionRejectsMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Gate)
	}{
		{"state", func(g *Gate) { g.State = "ready" }},
		{"reader", func(g *Gate) { g.MinimumReader = 12 }},
		{"instance", func(g *Gate) { g.Head.InstanceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" }},
		{"path", func(g *Gate) { g.Authority += "-other" }},
		{"prefix", func(g *Gate) { g.Head.Hash = strings.Repeat("a", 64) }},
		{"ahead", func(g *Gate) { g.Head.Sequence = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, a, _, g := maintenanceFixture(t)
			tc.change(&g)
			raw, _ := json.Marshal(g)
			if _, e := c.ExecContext(context.Background(), "UPDATE recovery_gate SET value=?", string(raw)); e != nil {
				t.Fatal(e)
			}
			if _, e := AdmitMaintenance(context.Background(), c, m.database, a); e == nil {
				t.Fatal("admitted")
			}
		})
	}
	m, c, a, _, _ := maintenanceFixture(t)
	if _, e := AdmitMaintenance(context.Background(), c, m.database+"-wrong", a); e == nil {
		t.Fatal("wrong database admitted")
	}
	if e := independentMaintenancePath(filepath.Join(filepath.Dir(a.path), "main.sqlite"), a.path); e == nil {
		t.Fatal("contained authority admitted")
	}
	link := filepath.Join(filepath.Dir(a.path), "authority-link")
	if e := os.Symlink(a.path, link); e != nil {
		t.Fatal(e)
	}
	if e := independentMaintenancePath(m.database, link); e == nil {
		t.Fatal("symlink admitted")
	}
}

func TestMaintenanceActualSourceAdmission(t *testing.T) {
	cmd := exec.Command("node", "--import", "tsx", "backend/internal/recovery/testdata/maintenance.mjs", t.TempDir())
	cmd.Dir = "../../.."
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("actual source oracle: %v\n%s", e, output)
	}
	var oracle struct {
		Database, Authority               string
		ExtensionRaw, ExtensionUpgraded   string
		Cases                             []struct{ Name, Raw, SourceGate, Combined string }
		MetadataCases                     []struct{ Name, Raw, Error, State string }
		Paths                             map[string]string
		RolledBackReader, CommittedReader int
	}
	if e = json.Unmarshal(output, &oracle); e != nil {
		t.Fatal(e, string(output))
	}
	if oracle.RolledBackReader != 1 || oracle.CommittedReader != 10 {
		t.Fatal("source reader behavior", oracle)
	}
	a, e := Open(context.Background(), oracle.Authority)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	db, e := sql.Open("sqlite3", oracle.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	c, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	for _, tc := range oracle.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if _, e := c.ExecContext(context.Background(), "UPDATE recovery_gate SET value=?", tc.Raw); e != nil {
				t.Fatal(e)
			}
			_, e := AdmitMaintenance(context.Background(), c, oracle.Database, a)
			got := ""
			if e != nil {
				got = e.Error()
			}
			if got != tc.Combined {
				t.Fatalf("source gate=%q, source gate+reconcile prefix=%q, Go=%q", tc.SourceGate, tc.Combined, got)
			}
		})
	}
	for _, tc := range oracle.MetadataCases {
		t.Run("metadata/"+tc.Name, func(t *testing.T) {
			if _, e := c.ExecContext(context.Background(), "UPDATE recovery_gate SET value=?", tc.Raw); e != nil {
				t.Fatal(e)
			}
			g, _, e := maintenanceMetadata(context.Background(), c)
			got := ""
			if e != nil {
				got = e.Error()
			}
			if got != tc.Error {
				t.Fatalf("source metadata=%q Go=%q", tc.Error, got)
			}
			live, e := ReadGate(context.Background(), c)
			wantLive := tc.Error == "" && tc.State == "ready" && g.MinimumReader <= ReaderCapability
			if (e == nil) != wantLive {
				t.Fatalf("live ready acceptance=%v error=%v", wantLive, e)
			}
			if wantLive && live != g {
				t.Fatalf("live/maintenance metadata differ: %+v %+v", live, g)
			}
		})
	}
	if _, e := c.ExecContext(context.Background(), "UPDATE recovery_gate SET value=?", oracle.ExtensionRaw); e != nil {
		t.Fatal(e)
	}
	m, e := AdmitMaintenance(context.Background(), c, oracle.Database, a)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Apply(context.Background(), 10, func(*MaintenanceTx) error { return nil }); e != nil {
		t.Fatal(e)
	}
	var upgraded string
	if e = c.QueryRowContext(context.Background(), "SELECT value FROM recovery_gate WHERE id=1").Scan(&upgraded); e != nil {
		t.Fatal(e)
	}
	if upgraded != oracle.ExtensionUpgraded {
		t.Fatalf("UTF-16 extension upgrade differs from actual source:\nGo: %s\nTS: %s", upgraded, oracle.ExtensionUpgraded)
	}
	for name, path := range map[string]string{"contained": filepath.Join(filepath.Dir(oracle.Database), "authority"), "lexicalDots": filepath.Join(filepath.Dir(oracle.Database), "..authority"), "independent": oracle.Authority, "symlink": filepath.Join(filepath.Dir(oracle.Authority), "linked")} {
		e := independentMaintenancePath(oracle.Database, path)
		got := ""
		if e != nil {
			got = e.Error()
		}
		if got != oracle.Paths[name] {
			t.Fatalf("path %s: Go %q source %q", name, got, oracle.Paths[name])
		}
	}
}

func TestMaintenanceMainReservationPrecedesAuthority(t *testing.T) {
	m, c, a, _, _ := maintenanceFixture(t)
	ctx := context.Background()
	db, e := sql.Open("sqlite3", m.database)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	writer, e := db.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Close()
	if _, e = writer.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	defer rollbackOwned(writer)
	if _, e = c.ExecContext(ctx, "PRAGMA busy_timeout=1000"); e != nil {
		t.Fatal(e)
	}
	completed := make(chan error, 1)
	go func() { completed <- m.Apply(ctx, 10, effect("after writer")) }()
	// Give the maintenance operation time to block on main's reservation. It
	// must not own authority: the already-reserved main writer acquires it next.
	time.Sleep(30 * time.Millisecond)
	lockctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	e = a.mu.LockContext(lockctx)
	if e == nil {
		a.mu.Unlock()
	}
	if e != nil {
		rollbackOwned(writer)
		<-completed
		t.Fatalf("main/authority lock inversion: %v", e)
	}
	if e = rollbackOwned(writer); e != nil {
		t.Fatal(e)
	}
	if e = <-completed; e != nil {
		t.Fatal(e)
	}
	// Waiting for authority after main reservation is context-cancellable and
	// releases the owned main transaction when canceled.
	a.mu.Lock()
	waitctx, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	e = m.Apply(waitctx, 10, effect("never"))
	stop()
	a.mu.Unlock()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	active, e := transactionActive(c)
	if e != nil || active {
		t.Fatal("reservation leaked", active, e)
	}
}

func TestMaintenanceMetadataPreservesExtensionAndMissingGate(t *testing.T) {
	m, c, a, _, _ := maintenanceFixture(t)
	ctx := context.Background()
	_, e := c.ExecContext(ctx, `UPDATE recovery_gate SET value=json_set(value,'$.extension',json('{"large":90071992547409931234,"extra":true}'))`)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Apply(ctx, 10, effect("upgraded")); e != nil {
		t.Fatal(e)
	}
	var raw string
	if e = c.QueryRowContext(ctx, "SELECT value FROM recovery_gate").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(raw, `"large":90071992547409940000`) || !strings.Contains(raw, `"extra":true`) {
		t.Fatal("extension differs from source JSON.parse/stringify", raw)
	}
	if _, e = c.ExecContext(ctx, "DELETE FROM recovery_gate"); e != nil {
		t.Fatal(e)
	}
	if _, e = AdmitMaintenance(ctx, c, m.database, a); e == nil || e.Error() != "recovery_gate_invalid" {
		t.Fatal(e)
	}
	if _, e = c.ExecContext(ctx, "DROP TABLE recovery_gate"); e != nil {
		t.Fatal(e)
	}
	if _, e = AdmitMaintenance(ctx, c, m.database, a); e == nil || e.Error() != "recovery_maintenance_denied" {
		t.Fatal(e)
	}
}

func TestMaintenanceNestedNativeRollbackCannotAutocommit(t *testing.T) {
	m, c, _, _, g := maintenanceFixture(t)
	ctx := context.Background()
	_, e := c.ExecContext(ctx, `CREATE TRIGGER abort_effect BEFORE INSERT ON effects WHEN new.value='abort' BEGIN SELECT RAISE(ROLLBACK,'native rollback'); END`)
	if e != nil {
		t.Fatal(e)
	}
	e = m.Apply(ctx, 10, func(tx *MaintenanceTx) error {
		if e := effect("outer")(tx); e != nil {
			return e
		}
		if e := tx.Apply(ctx, 11, effect("abort")); e == nil {
			t.Fatal("expected native rollback")
		}
		// Deliberately swallow the nested error, then try a normal domain write.
		if _, e := tx.ExecContext(ctx, "INSERT INTO effects VALUES('must not autocommit')"); e == nil {
			t.Fatal("autocommit escaped")
		}
		return nil
	})
	if e == nil || !strings.Contains(e.Error(), "transaction_lost") {
		t.Fatal(e)
	}
	var count int
	if e = c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	after, _, e := maintenanceMetadata(ctx, c)
	if e != nil || after != g {
		t.Fatal(after, e)
	}
	// A new independently owned effect can proceed after the previous native
	// rollback, without retaining the earlier poisoned transaction state.
	if e = m.Apply(ctx, 10, effect("new effect")); e != nil {
		t.Fatal(e)
	}
}

func TestMaintenanceDeferredRowsRollbackAndExpiry(t *testing.T) {
	m, c, _, _, g := maintenanceFixture(t)
	ctx := context.Background()
	_, e := c.ExecContext(ctx, `CREATE TRIGGER abort_effect BEFORE INSERT ON effects WHEN new.value='abort' BEGIN SELECT RAISE(ROLLBACK,'native rollback'); END`)
	if e != nil {
		t.Fatal(e)
	}
	e = m.Apply(ctx, 10, func(tx *MaintenanceTx) error {
		_ = tx.Apply(ctx, 11, func(inner *MaintenanceTx) error {
			rows, e := inner.QueryContext(ctx, "INSERT INTO effects VALUES('abort') RETURNING value")
			if e != nil {
				return e
			}
			defer rows.Close()
			if rows.Next() {
				t.Fatal("unexpected returning row")
			}
			return rows.Err()
		})
		if _, e := tx.ExecContext(ctx, "INSERT INTO effects VALUES('escaped')"); e == nil {
			t.Fatal("deferred rollback escaped")
		}
		return nil
	})
	if e == nil {
		t.Fatal("native deferred rollback accepted")
	}
	var count int
	c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&count)
	if count != 0 {
		t.Fatal(count)
	}
	after, _, e := maintenanceMetadata(ctx, c)
	if e != nil || after != g {
		t.Fatal(after, e)
	}
	var escaped *MaintenanceRow
	var escapedRows *MaintenanceRows
	e = m.Apply(ctx, 10, func(tx *MaintenanceTx) error {
		escaped = tx.QueryRowContext(ctx, "INSERT INTO effects VALUES('late') RETURNING value")
		var err error
		escapedRows, err = tx.QueryContext(ctx, "SELECT 1")
		return err
	})
	if e != nil {
		t.Fatal(e)
	}
	var value string
	if e = escaped.Scan(&value); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("escaped row executed", e)
	}
	if escapedRows.Next() {
		t.Fatal("escaped rows advanced")
	}
	c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&count)
	if count != 0 {
		t.Fatal(count)
	}
}
