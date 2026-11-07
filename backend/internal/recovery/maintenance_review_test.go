package recovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// JSON.parse is case-sensitive, replaces duplicate objects, and retains null.
func TestMaintenanceReviewMetadataJSONSemantics(t *testing.T) {
	for _, name := range []string{"caseFold", "duplicateHead", "duplicateNullState", "caseDistinctExtension"} {
		t.Run(name, func(t *testing.T) {
			m, c, a, _, g := maintenanceFixture(t)
			raw, err := json.Marshal(g)
			if err != nil {
				t.Fatal(err)
			}
			value := string(raw)
			wantAdmission := false
			switch name {
			case "caseFold":
				value = strings.Replace(value, `"minimumReader":`, `"MINIMUMREADER":`, 1)
			case "duplicateHead":
				value = strings.TrimSuffix(value, "}") + `,"head":{"sequence":0}}`
			case "duplicateNullState":
				value = strings.TrimSuffix(value, "}") + `,"state":null}`
			case "caseDistinctExtension":
				value = strings.TrimSuffix(value, "}") + `,"State":"ready"}`
				wantAdmission = true
			}
			if _, err = c.ExecContext(context.Background(), "UPDATE recovery_gate SET value=?", value); err != nil {
				t.Fatal(err)
			}
			_, err = AdmitMaintenance(context.Background(), c, m.database, a)
			if (err == nil) != wantAdmission {
				t.Fatalf("source metadata admission=%v, Go error=%v, raw=%s", wantAdmission, err, value)
			}
		})
	}
}

func TestMaintenanceReviewCallerDeferredTransactionReservesMain(t *testing.T) {
	m, c, _, _, _ := maintenanceFixture(t)
	ctx := context.Background()
	db, err := sql.Open("sqlite3", m.database+"?_busy_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = c.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer rollbackOwned(c)
	if err = m.Apply(ctx, 1, func(tx *MaintenanceTx) error {
		// This callback already owns authority. Another connection must not be
		// able to reserve main, or a main-first writer can invert the lock order.
		_, reserveErr := writer.ExecContext(ctx, "BEGIN IMMEDIATE")
		if reserveErr == nil {
			t.Error("authority is held but main is still available for another writer's reservation")
			return rollbackOwned(writer)
		}
		if !strings.Contains(reserveErr.Error(), "locked") {
			return reserveErr
		}
		return effect("caller-owned")(tx)
	}); err != nil {
		t.Fatal(err)
	}
	active, err := transactionActive(c)
	if err != nil || !active {
		t.Fatalf("caller transaction ended: active=%v err=%v", active, err)
	}
	if err = rollbackOwned(c); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&count); err != nil || count != 0 {
		t.Fatalf("caller rollback lost ownership: count=%d err=%v", count, err)
	}
}

// Metadata's full source-safe range is independent of the Go platform's int
// width. Future-reader values must never wrap into a supported capability.
func TestMaintenanceReviewReaderIntegerWidth(t *testing.T) {
	m, c, a, _, base := maintenanceFixture(t)
	ctx := context.Background()
	for _, reader := range []int64{2147483648, 4294967297, 9007199254740991, 9007199254740992} {
		g := base
		g.MinimumReader = reader
		raw, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		decoded, _, err := decodeGate(string(raw))
		if reader <= 9007199254740991 {
			if err != nil || decoded.MinimumReader != reader {
				t.Fatalf("reader=%d decoded=%d err=%v", reader, decoded.MinimumReader, err)
			}
		} else if err == nil {
			t.Fatal("unsafe integer admitted as metadata")
		}
		if _, err = c.ExecContext(ctx, "UPDATE recovery_gate SET value=?", string(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err = AdmitMaintenance(ctx, c, m.database, a); err == nil {
			t.Fatalf("future reader %d admitted", reader)
		}
		g.State = "ready"
		raw, err = json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.ExecContext(ctx, "UPDATE recovery_gate SET value=?", string(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err = ReadGate(ctx, c); err == nil {
			t.Fatalf("future ready reader %d admitted", reader)
		}
	}
}
