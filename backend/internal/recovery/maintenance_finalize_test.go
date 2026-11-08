package recovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestFinalizeVerifiedAtomicGateHoldAndReader(t *testing.T) {
	m, c, _, _, gate := maintenanceFixture(t)
	ctx := context.Background()
	if _, e := m.FinalizeVerified(ctx, nil); e == nil {
		t.Fatal("nil verification accepted")
	}
	head, e := m.FinalizeVerified(ctx, func(tx *MaintenanceTx) error {
		return tx.Apply(ctx, 11, effect("verified"))
	})
	if e != nil || head != gate.Head {
		t.Fatal(head, e)
	}
	got, _, e := maintenanceMetadata(ctx, c)
	if e != nil || got.State != "ready" || got.MinimumReader != 11 || got.Head != head {
		t.Fatal(got, e)
	}
	var status, restore string
	if e = c.QueryRowContext(ctx, "SELECT restore_id,status FROM recovery_model_hold").Scan(&restore, &status); e != nil || status != "held" || !uuid4Pattern.MatchString(restore) {
		t.Fatal(restore, status, e)
	}
	if _, e = m.FinalizeVerified(ctx, func(*MaintenanceTx) error { return nil }); e == nil {
		t.Fatal("reused ready admission")
	}
}

func TestFinalizeFailureLeavesCommittedReplayAndQuarantine(t *testing.T) {
	m, c, _, _, gate := maintenanceFixture(t)
	ctx := context.Background()
	if e := m.Apply(ctx, 10, effect("prior receipt")); e != nil {
		t.Fatal(e)
	}
	boom := errors.New("missing material")
	_, e := m.FinalizeVerified(ctx, func(tx *MaintenanceTx) error {
		if e := effect("unverified")(tx); e != nil {
			return e
		}
		return boom
	})
	if !errors.Is(e, boom) {
		t.Fatal(e)
	}
	got, _, e := maintenanceMetadata(ctx, c)
	if e != nil || got.Head != gate.Head || got.State != "quarantine" || got.MinimumReader != 10 {
		t.Fatal(got, e)
	}
	var count int
	if e = c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}

func TestFinalizeCaughtNativeLossNeverWritesReady(t *testing.T) {
	m, c, _, _, _ := maintenanceFixture(t)
	ctx := context.Background()
	if _, e := c.ExecContext(ctx, `CREATE TRIGGER lose BEFORE INSERT ON effects BEGIN SELECT RAISE(ROLLBACK,'lost'); END`); e != nil {
		t.Fatal(e)
	}
	_, e := m.FinalizeVerified(ctx, func(tx *MaintenanceTx) error { _ = effect("lost")(tx); return nil })
	if e == nil || !strings.Contains(e.Error(), "recovery_main_transaction_lost") {
		t.Fatal(e)
	}
	got, _, e := maintenanceMetadata(ctx, c)
	if e != nil || got.State != "quarantine" {
		t.Fatal(got, e)
	}
	var exists bool
	if e = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='recovery_model_hold')").Scan(&exists); e != nil || exists {
		t.Fatal(exists, e)
	}
}

func TestFinalizeCaughtOrdinaryErrorPreservesTransaction(t *testing.T) {
	m, c, _, _, _ := maintenanceFixture(t)
	ctx := context.Background()
	_, e := m.FinalizeVerified(ctx, func(tx *MaintenanceTx) error {
		_, _ = tx.ExecContext(ctx, "INSERT INTO absent_table VALUES(1)")
		return effect("usable")(tx)
	})
	if e != nil {
		t.Fatal(e)
	}
	var n int
	if e = c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func TestFinalizeNestedRollbackRetainsQuarantine(t *testing.T) {
	m, c, _, _, gate := maintenanceFixture(t)
	ctx := context.Background()
	if _, e := c.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	if _, e := m.FinalizeVerified(ctx, func(tx *MaintenanceTx) error { return effect("verified")(tx) }); e != nil {
		t.Fatal(e)
	}
	if _, e := c.ExecContext(ctx, "ROLLBACK"); e != nil {
		t.Fatal(e)
	}
	got, _, e := maintenanceMetadata(ctx, c)
	if e != nil || got != gate {
		t.Fatal(got, e)
	}
}

func TestFinalizeRejectsMalformedPriorHold(t *testing.T) {
	m, c, _, _, _ := maintenanceFixture(t)
	ctx := context.Background()
	if _, e := c.ExecContext(ctx, "CREATE TABLE recovery_model_hold(id INTEGER,restore_id TEXT,status TEXT)"); e != nil {
		t.Fatal(e)
	}
	if _, e := m.FinalizeVerified(ctx, func(*MaintenanceTx) error { return nil }); e == nil || !strings.Contains(e.Error(), "recovery_model_hold_invalid") {
		t.Fatal(e)
	}
	gate, _, e := maintenanceMetadata(ctx, c)
	if e != nil || gate.State != "quarantine" {
		t.Fatal(gate, e)
	}
}

func TestFinalizeUncertainCommitIsNotRetried(t *testing.T) {
	m, c, _, _, gate := maintenanceFixture(t)
	ctx := context.Background()
	calls := 0
	h, e := m.finalizeVerified(ctx, func(tx *MaintenanceTx) error { return effect("once")(tx) }, func(ctx context.Context, c *sql.Conn, _ string) error {
		calls++
		if _, e := c.ExecContext(ctx, "COMMIT"); e != nil {
			return e
		}
		return errors.New("synthetic postcommit I/O")
	})
	if !errors.Is(e, ErrCommitOutcomeUnknown) || h != (Head{}) || calls != 1 {
		t.Fatal(h, e, calls)
	}
	got, _, e := maintenanceMetadata(ctx, c)
	if e != nil || got.State != "ready" || got.Head != gate.Head {
		t.Fatal(got, e)
	}
	var n int
	if e = c.QueryRowContext(ctx, "SELECT count(*) FROM effects").Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestAuditedHistoryAndFinalizeRejectChangedHead(t *testing.T) {
	m, c, a, _, gate := maintenanceFixture(t)
	ctx := context.Background()
	entries, e := m.History(ctx)
	if e != nil || len(entries) != 0 {
		t.Fatal(entries, e)
	}
	external, e := sql.Open("sqlite3", a.path+"/authority.sqlite")
	if e != nil {
		t.Fatal(e)
	}
	defer external.Close()
	payload := `{"kind":"source","sceneId":"s","messageId":"m"}`
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s|1|%s|%s", gate.Head.InstanceID, gate.Head.Hash, payload))))
	if _, e = external.Exec("INSERT INTO revocations VALUES(1,?,?,?)", gate.Head.Hash, payload, digest); e != nil {
		t.Fatal(e)
	}
	if e = m.CheckHead(ctx); e == nil || !strings.Contains(e.Error(), "recovery_authority_head_changed") {
		t.Fatal(e)
	}
	if _, e = m.History(ctx); e == nil {
		t.Fatal("stale history accepted")
	}
	if _, e = m.FinalizeVerified(ctx, func(*MaintenanceTx) error { t.Fatal("stale verifier invoked"); return nil }); e == nil {
		t.Fatal("stale gate ACK")
	}
	got, _, e := maintenanceMetadata(ctx, c)
	if e != nil || got != gate {
		t.Fatal(got, e)
	}
}

func TestReconcileConnectionRejectsCallerTransaction(t *testing.T) {
	m, c, _, _, _ := maintenanceFixture(t)
	ctx := context.Background()
	if _, e := c.ExecContext(ctx, "PRAGMA journal_mode=WAL;BEGIN IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	if e := m.PrepareReconciliationConnection(ctx); e == nil || !strings.Contains(e.Error(), "recovery_reconcile_transaction_active") {
		t.Fatal(e)
	}
	if _, e := c.ExecContext(ctx, "ROLLBACK"); e != nil {
		t.Fatal(e)
	}
	if e := m.PrepareReconciliationConnection(ctx); e != nil {
		t.Fatal(e)
	}
	var enabled bool
	if e := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); e != nil || !enabled {
		t.Fatal(enabled, e)
	}
}
