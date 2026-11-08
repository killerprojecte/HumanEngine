package recovery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestMaintenanceCaptureMultiplicityAndFailure(t *testing.T) {
	m, c, a, _, gate := maintenanceFixture(t)
	ctx := context.Background()
	var statement *MaintenanceStatement
	effects, e := m.CaptureEffects(ctx, func(tx *MaintenanceTx) error {
		var e error
		statement, e = tx.PrepareQuery(ctx, "SELECT value FROM effects")
		if e != nil {
			return e
		}
		for i := 0; i < 2; i++ {
			if e = tx.RecordReplayEffect(ctx, Assertion{ID: "a", ThroughVersion: 2}); e != nil {
				return e
			}
		}
		return effect("one")(tx)
	})
	if e != nil || len(effects) != 2 || effects[0] != effects[1] {
		t.Fatalf("multiplicity %v %v", effects, e)
	}
	if _, e = statement.Query(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped statement %v", e)
	}
	boom := errors.New("rollback")
	effects, e = m.CaptureEffects(ctx, func(tx *MaintenanceTx) error {
		if e := tx.RecordReplayEffect(ctx, Assertion{ID: "b", ThroughVersion: 3}); e != nil {
			return e
		}
		return boom
	})
	if !errors.Is(e, boom) || effects != nil {
		t.Fatalf("failed capture leaked %v %v", effects, e)
	}
	head, e := a.Head(ctx)
	if e != nil || head != gate.Head {
		t.Fatal(head, e)
	}
	var n int
	if e = c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestMaintenanceCaptureCaughtNativeLossCannotAutocommit(t *testing.T) {
	m, c, _, _, _ := maintenanceFixture(t)
	ctx := context.Background()
	if _, e := c.ExecContext(ctx, `CREATE TRIGGER capture_loss BEFORE INSERT ON effects WHEN NEW.value='lose' BEGIN SELECT RAISE(ROLLBACK,'capture_loss'); END`); e != nil {
		t.Fatal(e)
	}
	effects, e := m.CaptureEffects(ctx, func(tx *MaintenanceTx) error {
		_ = tx.Apply(ctx, 1, func(child *MaintenanceTx) error { return effect("lose")(child) })
		if e := effect("must-not-autocommit")(tx); e == nil {
			t.Fatal("write after native loss succeeded")
		}
		return nil
	})
	if e == nil || !strings.Contains(e.Error(), "recovery_main_transaction_lost") || effects != nil {
		t.Fatalf("loss %v %v", effects, e)
	}
	var n int
	if e = c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}

func TestMaintenanceSameScopeStatementErrors(t *testing.T) {
	for _, scenario := range []struct {
		name, raise string
		caught      bool
		wantRows    int
		wantError   bool
	}{{"caught-abort", "ABORT", true, 2, false}, {"uncaught-abort", "ABORT", false, 0, true}, {"caught-rollback", "ROLLBACK", true, 0, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			m, c, _, _, _ := maintenanceFixture(t)
			ctx := context.Background()
			_, e := c.ExecContext(ctx, "CREATE TRIGGER statement_failure BEFORE INSERT ON effects WHEN NEW.value='fail' BEGIN SELECT RAISE("+scenario.raise+",'statement_failure'); END")
			if e != nil {
				t.Fatal(e)
			}
			effects, e := m.CaptureEffects(ctx, func(tx *MaintenanceTx) error {
				if e := effect("before")(tx); e != nil {
					return e
				}
				e := effect("fail")(tx)
				if e == nil {
					t.Fatal("trigger did not fail")
				}
				if !scenario.caught {
					return e
				}
				after := effect("after")(tx)
				if scenario.raise == "ROLLBACK" {
					if after == nil {
						t.Fatal("autocommit after caught rollback")
					}
					return nil
				}
				if after != nil {
					return after
				}
				return tx.RecordReplayEffect(ctx, Assertion{ID: "survivor", ThroughVersion: 1})
			})
			if (e != nil) != scenario.wantError {
				t.Fatalf("error %v", e)
			}
			if scenario.wantError && effects != nil {
				t.Fatal("failed root receipts escaped")
			}
			if !scenario.wantError && len(effects) != 1 {
				t.Fatalf("caught ABORT lost receipt %v", effects)
			}
			var rows int
			if err := c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&rows); err != nil || rows != scenario.wantRows {
				t.Fatal(rows, err)
			}
		})
	}
}
