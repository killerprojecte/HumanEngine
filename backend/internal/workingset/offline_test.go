package workingset

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func offlinePinEvent(t *testing.T, p PinPolicy) string {
	t.Helper()
	raw, e := Encode(p)
	must(t, e)
	return `{"kind":"working_set_pin","schemaVersion":1,"policy":` + raw + `}`
}
func testOfflinePolicy(id string, pinned bool) PinPolicy {
	return PinPolicy{SchemaVersion: 1, SceneID: "scene", Selection: snap(id, "scene").Selection(), PolicyRevision: 1, Pinned: pinned}
}
func TestOfflineScopeLifetimeBindingAndNoAutostart(t *testing.T) {
	ctx := context.Background()
	f := offlineSetup(t, nil, []string{offlinePinEvent(t, testOfflinePolicy("a", true))})
	var held *OfflineMaintenance
	var callbackTx *recovery.MaintenanceTx
	owner, e := NewOfflineMaintenance(f.owner, OfflinePorts{ResolvePinTarget: func(_ context.Context, tx *recovery.MaintenanceTx, _ PinPolicy) (string, error) {
		callbackTx = tx
		return "stale", nil
	}})
	must(t, e)
	if e = owner.VerifyPinsInScope(ctx, nil); e == nil || e.Error() != "recovery_transaction_required" {
		t.Fatal(e)
	}
	must(t, f.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		held = owner.WithScope(ctx, tx)
		return held.VerifyPinsInScope(ctx, []string{offlinePinEvent(t, testOfflinePolicy("a", true))})
	}))
	if callbackTx == nil {
		t.Fatal("no actual maintenance callback")
	}
	if _, e = callbackTx.ExecContext(ctx, "SELECT 1"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	if e = held.ResetInScope(ctx); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("expired owner reopened transaction", e)
	}
	other := offlineSetup(t, nil, nil)
	must(t, other.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		_, e := owner.WithScope(ctx, tx).AssertSupported(ctx)
		if e == nil || !strings.Contains(e.Error(), "binding_mismatch") {
			t.Fatal(e)
		}
		return nil
	}))
}
func TestOfflineNestedRollbackAndResetPostcondition(t *testing.T) {
	ctx := context.Background()
	seed := func(live *fixture) {
		must(t, live.service.Transaction(ctx, func(s *Service) error {
			if e := s.Storage().Ensure(); e != nil {
				return e
			}
			if e := s.Storage().PutPin(testOfflinePolicy("a", false)); e != nil {
				return e
			}
			return s.Storage().PutItem(makeItem(snap("a", "scene"), nil, 10))
		}))
	}
	f := offlineSetup(t, seed, nil)
	owner, e := NewOfflineMaintenance(f.owner, OfflinePorts{})
	must(t, e)
	rollback := errors.New("inner rollback")
	must(t, f.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		_, e := tx.ExecContext(ctx, "CREATE TABLE effects(value);INSERT INTO effects VALUES('outer')")
		if e != nil {
			return e
		}
		e = tx.Apply(ctx, 1, func(child *recovery.MaintenanceTx) error {
			if e := owner.WithScope(ctx, child).ResetInScope(ctx); e != nil {
				return e
			}
			return rollback
		})
		if !errors.Is(e, rollback) {
			t.Fatal(e)
		}
		var n int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM memory_working_set_items").Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			t.Fatal("nested reset escaped rollback", n)
		}
		return nil
	}))
	_, e = f.conn.ExecContext(ctx, `CREATE TRIGGER prevent_reset BEFORE DELETE ON memory_working_set_items BEGIN SELECT RAISE(IGNORE); END`)
	must(t, e)
	e = owner.Reset(ctx)
	if e == nil || !strings.Contains(e.Error(), "recovery_working_set_reset_incomplete") {
		t.Fatal(e)
	}
	var n int
	must(t, f.conn.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&n))
	if n != 1 {
		t.Fatal(n)
	}
}
func TestOfflineNativeLossStopsCaughtErrorAutocommit(t *testing.T) {
	ctx := context.Background()
	f := offlineSetup(t, func(live *fixture) {
		must(t, live.service.Transaction(ctx, func(s *Service) error {
			if e := s.Storage().Ensure(); e != nil {
				return e
			}
			return s.Storage().PutItem(makeItem(snap("a", "scene"), nil, 10))
		}))
	}, nil)
	_, e := f.conn.ExecContext(ctx, `CREATE TRIGGER lose_workingset_reset BEFORE DELETE ON memory_working_set_items BEGIN SELECT RAISE(ROLLBACK,'lost'); END`)
	must(t, e)
	owner, e := NewOfflineMaintenance(f.owner, OfflinePorts{})
	must(t, e)
	e = f.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		_ = owner.WithScope(ctx, tx).ResetInScope(ctx)
		if _, e := tx.ExecContext(ctx, "INSERT INTO memory_working_set_receipts VALUES('escaped',1,'{}')"); e == nil {
			t.Fatal("autocommit after caught loss")
		}
		return nil
	})
	if e == nil {
		t.Fatal("transaction loss accepted")
	}
	var n int
	must(t, f.conn.QueryRowContext(ctx, "SELECT count(*) FROM memory_working_set_receipts").Scan(&n))
	if n != 0 {
		t.Fatal(n)
	}
}
func TestOfflineRejectsLiveMainAndDoesNotCreateAbsentTables(t *testing.T) {
	ctx := context.Background()
	f := offlineSetup(t, nil, nil)
	conn, e := f.live.db.Conn(ctx)
	must(t, e)
	defer conn.Close()
	var mainPath string
	rows, e := conn.QueryContext(ctx, "PRAGMA database_list")
	must(t, e)
	for rows.Next() {
		var seq int
		var name, path string
		must(t, rows.Scan(&seq, &name, &path))
		if name == "main" {
			mainPath = path
		}
	}
	must(t, rows.Err())
	must(t, rows.Close())
	if _, e = recovery.AdmitMaintenance(ctx, conn, mainPath, f.live.authority); e == nil {
		t.Fatal("live main admitted")
	}
	owner, e := NewOfflineMaintenance(f.owner, OfflinePorts{})
	must(t, e)
	must(t, owner.Reset(ctx))
	must(t, owner.VerifyPins(ctx, nil))
	exists, e := owner.AssertSupported(ctx)
	must(t, e)
	if exists {
		t.Fatal("empty replay created schema")
	}
}
func TestOfflinePinResolverUsesExactChildAndRollsBackPrefix(t *testing.T) {
	ctx := context.Background()
	a, b := testOfflinePolicy("a", false), testOfflinePolicy("b", true)
	events := []string{offlinePinEvent(t, a), offlinePinEvent(t, b)}
	f := offlineSetup(t, nil, events)
	var expected *recovery.MaintenanceTx
	owner, e := NewOfflineMaintenance(f.owner, OfflinePorts{ResolvePinTarget: func(_ context.Context, tx *recovery.MaintenanceTx, _ PinPolicy) (string, error) {
		if tx != expected {
			t.Fatal("resolver changed scope")
		}
		var n int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM memory_working_set_pins").Scan(&n); e != nil {
			return "", e
		}
		if n != 1 {
			t.Fatal("prior replay not visible", n)
		}
		return "missing", nil
	}})
	must(t, e)
	e = f.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		expected = tx
		return owner.WithScope(ctx, tx).VerifyPinsInScope(ctx, events)
	})
	if e == nil || !strings.Contains(e.Error(), "recovery_working_set_pin_material_missing") {
		t.Fatal(e)
	}
	exists, e := owner.AssertSupported(ctx)
	must(t, e)
	if exists {
		t.Fatal("lazy schema/prefix escaped rollback")
	}
}

func TestOfflineGlobAuditIgnoresNonmatchingUnderscoreNames(t *testing.T) {
	f := offlineSetup(t, func(live *fixture) {
		_, e := live.db.Exec("CREATE TABLE memoryXworkingXsetXsurprise(value TEXT)")
		must(t, e)
	}, nil)
	owner, e := NewOfflineMaintenance(f.owner, OfflinePorts{})
	must(t, e)
	exists, e := owner.AssertSupported(context.Background())
	must(t, e)
	if exists {
		t.Fatal("GLOB underscores became wildcards")
	}
	var n int
	must(t, f.conn.QueryRowContext(context.Background(), "SELECT count(*) FROM sqlite_master WHERE name='memoryXworkingXsetXsurprise'").Scan(&n))
	if n != 1 {
		t.Fatal("counterexample missing from actual backup")
	}
}

func TestOfflineResolverCaughtNativeLossCannotReplayInAutocommit(t *testing.T) {
	ctx := context.Background()
	events := []string{offlinePinEvent(t, testOfflinePolicy("a", true))}
	f := offlineSetup(t, func(live *fixture) {
		_, e := live.db.Exec(`CREATE TABLE resolver_effects(value); CREATE TRIGGER resolver_loss BEFORE INSERT ON resolver_effects BEGIN SELECT RAISE(ROLLBACK,'lost'); END`)
		must(t, e)
	}, events)
	called := false
	owner, e := NewOfflineMaintenance(f.owner, OfflinePorts{ResolvePinTarget: func(_ context.Context, tx *recovery.MaintenanceTx, _ PinPolicy) (string, error) {
		called = true
		_, err := tx.ExecContext(ctx, "INSERT INTO resolver_effects VALUES('must rollback')")
		if err == nil {
			t.Fatal("native rollback did not fail")
		}
		// A trusted callback may catch an ordinary error, but cannot restore a lost transaction.
		return "current", nil
	}})
	must(t, e)
	e = owner.VerifyPins(ctx, events)
	if !called || e == nil || !strings.Contains(e.Error(), "transaction_lost") {
		t.Fatal(called, e)
	}
	var n int
	must(t, f.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name GLOB 'memory_working_set_*'").Scan(&n))
	if n != 0 {
		t.Fatal("replay created schema after lost transaction", n)
	}
	must(t, f.conn.QueryRowContext(ctx, "SELECT count(*) FROM resolver_effects").Scan(&n))
	if n != 0 {
		t.Fatal("resolver effect escaped", n)
	}
}
