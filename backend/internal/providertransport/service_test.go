package providertransport

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestRestorationHoldBlocksEvenDisabledGate(t *testing.T) {
	db, e := sql.Open("sqlite3", filepath.Join(t.TempDir(), "synthetic.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(`CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT);INSERT INTO config VALUES('settings','{}');CREATE TABLE recovery_model_hold(id INTEGER PRIMARY KEY,restore_id TEXT,status TEXT);INSERT INTO recovery_model_hold VALUES(1,'12345678-1234-4123-8123-123456789012','held')`); e != nil {
		t.Fatal(e)
	}
	service := &BudgetService{DB: db}
	if _, e = service.Current(context.Background()); e == nil || e.Error() != "budget_restore_accounting_hold" {
		t.Fatal(e)
	}
	if _, e = db.Exec(`UPDATE recovery_model_hold SET status='manually_acknowledged'`); e != nil {
		t.Fatal(e)
	}
	gate, e := service.Current(context.Background())
	if e != nil || gate != nil {
		t.Fatalf("disabled gate %#v %v", gate, e)
	}
	if _, e = db.Exec(`ALTER TABLE recovery_model_hold ADD COLUMN unexpected TEXT`); e != nil {
		t.Fatal(e)
	}
	if _, e = service.Current(context.Background()); e == nil || e.Error() != "recovery_model_hold_invalid" {
		t.Fatal(e)
	}
}
func TestBudgetGateRejectsConfigurationChangeBeforeReservation(t *testing.T) {
	db, e := sql.Open("sqlite3", filepath.Join(t.TempDir(), "synthetic.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(`CREATE TABLE config(key TEXT PRIMARY KEY,value TEXT);INSERT INTO config VALUES('settings','{"budgetExperimentEnabled":true,"budgetMode":"best_effort"}')`); e != nil {
		t.Fatal(e)
	}
	service := &BudgetService{DB: db, Ledger: &SQLLedger{DB: db, BootID: "synthetic"}}
	gate, e := service.Current(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`UPDATE config SET value='{"budgetExperimentEnabled":false}'`); e != nil {
		t.Fatal(e)
	}
	_, e = gate.Prepare(context.Background(), Descriptor{Protocol: "openai-chat", Operation: "complete"}, map[string]any{})
	if e == nil || e.Error() != "budget_configuration_changed" {
		t.Fatal(e)
	}
}
