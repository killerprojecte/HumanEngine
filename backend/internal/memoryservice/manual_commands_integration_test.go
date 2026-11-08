package memoryservice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorymanual"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"reflect"
	"strings"
	"testing"
)

func TestManualOwnersGuardRollbackAndACK(t *testing.T) {
	for _, failure := range []string{"ABORT", "ROLLBACK"} {
		t.Run(failure, func(t *testing.T) {
			s, f := manualFixed(t)
			ctx := context.Background()
			command, e := s.manualCommands(memorymanual.Runtime{ID: func() (string, error) { return "manual-root", nil }, Now: func() string { return "2026-10-28T12:00:00.000Z" }})
			if e != nil {
				t.Fatal(e)
			}
			m, e := command.Create(ctx, parseObject(t, `{"scope":{"kind":"scene","sceneId":"s","personId":"p"},"kind":"fact","content":"Synthetic manual original"}`))
			if e != nil {
				t.Fatal(e)
			}
			id := text(m, "id")
			count := func(q string, args ...any) int {
				t.Helper()
				var n int
				if e := f.main.QueryRow(q, args...).Scan(&n); e != nil {
					t.Fatal(e)
				}
				return n
			}
			if count("SELECT count(*) FROM memory_fts WHERE assertion_id=?", id) != 1 {
				t.Fatal("actual projection missing")
			}
			if _, e = f.main.Exec("INSERT INTO vector_spaces VALUES('manual-space','{}'); INSERT INTO memory_vectors VALUES('manual-root','manual-space',X'01020304')"); e != nil {
				t.Fatal(e)
			}
			before := manualSnapshotDB(t, f.main)
			head, e := f.recovery.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.main.Exec(fmt.Sprintf("CREATE TRIGGER manual_receipt_failure BEFORE INSERT ON memory_command_receipts BEGIN SELECT RAISE(%s,'manual_receipt_failure'); END", failure)); e != nil {
				t.Fatal(e)
			}
			// Fails after update, real owner effects, revision checkpoint and notifications.
			_, e = command.UpdateGuarded(ctx, id, parseObject(t, `{"content":"Synthetic corrected"}`), memorymanual.Guard{ExpectedVersion: 1, IdempotencyKey: "retry"})
			if e == nil || !strings.Contains(e.Error(), "manual_receipt_failure") {
				t.Fatalf("wrong failure %v", e)
			}
			manualCompare(t, manualSnapshotDB(t, f.main), before)
			if count("SELECT count(*) FROM memory_vectors WHERE memory_id=?", id) != 1 || count("SELECT count(*) FROM memory_fts WHERE assertion_id=?", id) != 1 {
				t.Fatal("owner effects escaped rollback")
			}
			afterHead, e := f.recovery.Head(ctx)
			if e != nil || afterHead != head {
				t.Fatalf("unexpected authority write %v %v", afterHead, e)
			}
			if _, e = f.main.Exec("DROP TRIGGER manual_receipt_failure"); e != nil {
				t.Fatal(e)
			}
			guard := memorymanual.Guard{ExpectedVersion: 1, IdempotencyKey: "retry"}
			patch := parseObject(t, `{"content":"Synthetic corrected"}`)
			result, e := command.UpdateGuarded(ctx, id, patch, guard)
			if e != nil {
				t.Fatal(e)
			}
			snapshot := manualSnapshotDB(t, f.main)
			replay, e := command.UpdateGuarded(ctx, id, patch, guard)
			if e != nil || !deepEqual(result, replay) {
				t.Fatalf("replay %v", e)
			}
			manualCompare(t, manualSnapshotDB(t, f.main), snapshot)
			if count("SELECT count(*) FROM memory_vectors WHERE memory_id=?", id) != 0 || count("SELECT count(*) FROM memory_deleted") != 1 || count("SELECT count(*) FROM memory_assertion_revisions WHERE assertion_id=?", id) != 2 {
				t.Fatal("correction cleanup/history wrong")
			}
			if _, e = command.Update(ctx, id, parseObject(t, `{"status":"invalidated"}`)); e != nil {
				t.Fatal(e)
			}
			if count("SELECT count(*) FROM memory_fts WHERE assertion_id=?", id) != 0 || count("SELECT count(*) FROM memory_person_projection_state WHERE assertion_id=?", id) != 0 {
				t.Fatal("unavailable projections retained")
			}
			gate, e := recovery.ReadGate(ctx, f.main)
			if e != nil {
				t.Fatal(e)
			}
			finalHead, e := f.recovery.Head(ctx)
			if e != nil || gate.Head != finalHead || finalHead.Sequence <= head.Sequence || gate.MinimumReader < 11 {
				t.Fatalf("eligibility ACK absent %+v %+v %v", gate, finalHead, e)
			}
			replayed, replayError := command.UpdateGuarded(ctx, id, patch, guard)
			if replayError != nil || text(replayed, "status") != "invalidated" {
				t.Fatalf("same-version retry must return current body: %v", replayError)
			}
		})
	}
}
func TestManualReceiptSourceRetentionAndMetadataOnly(t *testing.T) {
	s, f := manualFixed(t)
	ctx := context.Background()
	m, e := s.Create(ctx, parseObject(t, `{"scope":{"kind":"scene","sceneId":"s"},"kind":"fact","content":"Synthetic secret body"}`))
	if e != nil {
		t.Fatal(e)
	}
	id := text(m, "id")
	// Old metadata is fixture setup. The real command performs the source policy
	// eviction, and old retry preconditions still prevent duplicated changes.
	tx, e := f.main.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4096; i++ {
		if _, e = tx.Exec("INSERT INTO memory_command_receipts VALUES(?,?,?,?,?,?)", fmt.Sprintf("old-%d", i), id, "hash", 0, 1, "old"); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	_, e = s.UpdateGuarded(ctx, id, parseObject(t, `{"locked":true}`), memorymanual.Guard{ExpectedVersion: 1, IdempotencyKey: "new"})
	if e != nil {
		t.Fatal(e)
	}
	var count int
	if e = f.main.QueryRow("SELECT count(*) FROM memory_command_receipts").Scan(&count); e != nil || count != 4096 {
		t.Fatalf("retention %d %v", count, e)
	}
	if e = f.main.QueryRow("SELECT count(*) FROM memory_command_receipts WHERE command_key='old-0'").Scan(&count); e != nil || count != 0 {
		t.Fatal("oldest receipt retained", e)
	}
	rows, e := f.main.Query("SELECT * FROM memory_command_receipts LIMIT 1")
	if e != nil {
		t.Fatal(e)
	}
	cols, e := rows.Columns()
	rows.Close()
	if e != nil || !reflect.DeepEqual(cols, []string{"command_key", "assertion_id", "payload_hash", "deletion_epoch", "result_version", "created_at"}) {
		t.Fatal("body retained in metadata schema", cols, e)
	}
	_, e = s.UpdateGuarded(ctx, id, parseObject(t, `{"locked":false}`), memorymanual.Guard{ExpectedVersion: 1, IdempotencyKey: "old-0"})
	if e == nil || e.Error() != "assertion_version_conflict" {
		t.Fatalf("evicted retry %v", e)
	}
}
func TestManualTypedScopeExpiresAndNativeLossCannotAutocommit(t *testing.T) {
	_, f := manualFixed(t)
	ctx := context.Background()
	var captured *assertions.Store
	if e := f.store.Transaction(ctx, func(st *assertions.Store) error { captured = st; return nil }); e != nil {
		t.Fatal(e)
	}
	if e := captured.DeleteManualSupports("none"); e == nil {
		t.Fatal("expired owner accepted")
	}
	if _, e := f.main.Exec(`CREATE TRIGGER manual_loss BEFORE INSERT ON memory_command_receipts BEGIN SELECT RAISE(ROLLBACK,'manual_native_loss'); END`); e != nil {
		t.Fatal(e)
	}
	var after error
	e := f.store.Transaction(ctx, func(st *assertions.Store) error {
		first := st.SaveManualReceipt("lost", assertions.ManualCommandReceipt{AssertionID: "none", PayloadHash: "hash"}, "now")
		if first == nil || !strings.Contains(first.Error(), "manual_native_loss") {
			t.Fatalf("wrong native loss %v", first)
		}
		after = st.DeleteManualSupports("none")
		return nil
	})
	if e == nil || !errors.Is(after, sql.ErrTxDone) {
		t.Fatalf("loss accepted subsequent write: outer=%v after=%v", e, after)
	}
}

func TestManualGuardFirstWriteCreatesReceiptSchema(t *testing.T) {
	s, f := serviceSetup(t) // Unlike manualFixed, this does not initialize receipts.
	ctx := context.Background()
	exists := func() bool {
		t.Helper()
		var n int
		if e := f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='memory_command_receipts'").Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n != 0
	}
	if exists() {
		t.Fatal("fixture already initialized manual receipts")
	}
	m, e := s.Create(ctx, parseObject(t, `{"scope":{"kind":"scene","sceneId":"s"},"kind":"fact","content":"Synthetic first guarded command"}`))
	if e != nil {
		t.Fatal(e)
	}
	if exists() {
		t.Fatal("unguarded create unexpectedly initialized receipts")
	}
	id := text(m, "id")
	patch := parseObject(t, `{"locked":true}`)
	guard := memorymanual.Guard{ExpectedVersion: 1, IdempotencyKey: "first"}
	result, e := s.UpdateGuarded(ctx, id, patch, guard)
	if e != nil {
		t.Fatal(e)
	}
	if !exists() {
		t.Fatal("first guard did not initialize metadata schema")
	}
	replay, e := s.UpdateGuarded(ctx, id, patch, guard)
	if e != nil || !deepEqual(result, replay) {
		t.Fatal("first receipt cannot replay", e)
	}
	var n int
	if e = f.main.QueryRow("SELECT count(*) FROM memory_command_receipts WHERE command_key='first'").Scan(&n); e != nil || n != 1 {
		t.Fatalf("first receipt %d %v", n, e)
	}
}
