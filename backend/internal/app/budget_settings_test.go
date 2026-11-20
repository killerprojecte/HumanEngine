package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestFreshBudgetSettingsCreateSecurityState(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{}
	options.Installation.Database = filepath.Join(filepath.Dir(options.Installation.Database), "humanengine.sqlite")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	a, err := Open(ctx, options)
	if a != nil {
		t.Cleanup(func() {
			if err := a.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", options.Installation.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := sql.Open("sqlite3", filepath.Join(options.Installation.AuthorityDirectory, "authority.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	var table int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='recovery_security_state'").Scan(&table); err != nil || table != 0 {
		t.Fatalf("fresh installation must not have a security-state table: %d %v", table, err)
	}
	before, err := recovery.ReadGate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	settings := appRequest(t, a, http.MethodGet, "/api/settings", nil).(*jsjson.Object)
	set := func(k string, v any) {
		t.Helper()
		if err := settings.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	put := func() {
		t.Helper()
		wire, err := jsjson.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		appRequest(t, a, http.MethodPut, "/api/settings", json.RawMessage(wire))
	}
	set("budgetExperimentEnabled", true)
	set("budgetMode", "best_effort")
	set("budgetDailyUsd", 2.0000000001)
	put()
	after, err := recovery.ReadGate(ctx, db)
	if err != nil || after.Head.Sequence != before.Head.Sequence+1 || after.Authority != before.Authority {
		t.Fatalf("fresh settings did not ACK exactly one security intent: %+v %v", after, err)
	}
	var revision, payload string
	if err := db.QueryRowContext(ctx, "SELECT revision FROM recovery_security_state WHERE id=1").Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := ledger.QueryRowContext(ctx, "SELECT payload FROM revocations WHERE sequence=?", after.Head.Sequence).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"security_state","revision":"` + revision + `"}`
	if payload != want {
		t.Fatalf("unexpected security event %q, want %q", payload, want)
	}
	budget := appRequest(t, a, http.MethodGet, "/api/budget", nil)
	if field(budget, "enabled") != true || field(budget, "mode") != "best_effort" || field(budget, "remainingNanoUsd") != float64(2000000001) {
		t.Fatalf("new policy not visible: %+v", budget)
	}
	put()
	unchanged, err := recovery.ReadGate(ctx, db)
	if err != nil || unchanged.Head != after.Head {
		t.Fatalf("repeated PUT changed the durable head: %+v %v", unchanged, err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE recovery_model_hold(id INTEGER PRIMARY KEY, restore_id TEXT, status TEXT);
		INSERT INTO recovery_model_hold VALUES(1,'12345678-1234-4234-8234-123456789012','held')`); err != nil {
		t.Fatal(err)
	}
	set("budgetDailyUsd", float64(3))
	put()
	hold := appRequest(t, a, http.MethodGet, "/api/budget/recovery", nil)
	if field(hold, "held") != true || field(hold, "restorationCoverage") != "unknown" {
		t.Fatalf("settings write released restored hold: %+v", hold)
	}
	if _, err := a.models.Client.Budgets.Current(ctx); err == nil || err.Error() != "budget_restore_accounting_hold" {
		t.Fatalf("settings write altered provider admission: %v", err)
	}
}
