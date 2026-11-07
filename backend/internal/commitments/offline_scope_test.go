package commitments

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"strings"
	"testing"
)

func offlineFixture(t *testing.T) (*fixture, *sql.Conn, *recovery.Maintenance) {
	t.Helper()
	f := seeded(t)
	var raw string
	must(t, f.main.QueryRow("SELECT value FROM recovery_gate").Scan(&raw))
	gate := object(parse(t, raw))
	_ = gate.Set("state", "quarantine")
	_, e := f.main.Exec("UPDATE recovery_gate SET value=?", stringify(t, gate))
	must(t, e)
	path := mainPath(t, f.main)
	conn, e := f.main.Conn(context.Background())
	must(t, e)
	t.Cleanup(func() { conn.Close() })
	owner, e := recovery.AdmitMaintenance(context.Background(), conn, path, f.ledger)
	must(t, e)
	return f, conn, owner
}
func boundOffline(t *testing.T, f *fixture, owner *recovery.Maintenance, tx *recovery.MaintenanceTx, ctx context.Context, owners Owners) *PrivacyService {
	t.Helper()
	store := &Store{options: f.store.options, maintenance: &maintenanceScope{owner, tx, ctx}}
	service, e := NewPrivacyService(store, owners)
	must(t, e)
	return service
}
func readerVersion(t *testing.T, conn *sql.Conn) float64 {
	t.Helper()
	var raw string
	must(t, conn.QueryRowContext(context.Background(), "SELECT value FROM recovery_gate").Scan(&raw))
	return num(get(parse(t, raw), "minimumReader"))
}
func noNewOfflineIntents(t *testing.T, f *fixture) {
	t.Helper()
	var n int
	must(t, f.external.QueryRow("SELECT count(*) FROM revocations").Scan(&n))
	if n != 0 {
		t.Fatalf("offline replay created %d durable events", n)
	}
}
func TestOfflineScopeBindingExpirationAndLiveSeparation(t *testing.T) {
	f, _, owner := offlineFixture(t)
	_, _, other := offlineFixture(t)
	ctx := context.Background()
	var escaped *Store
	var escapedTx *recovery.MaintenanceTx
	must(t, owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		if e := tx.CheckBinding(ctx, other); e == nil {
			t.Fatal("wrong maintenance root accepted")
		}
		wrong := &Store{options: f.store.options, maintenance: &maintenanceScope{other, tx, ctx}}
		if _, e := wrong.reader().Head("commitment-1"); e == nil {
			t.Fatal("wrong-root adapter read accepted")
		}
		s := boundOffline(t, f, owner, tx, ctx, f.service.owners)
		escaped = s.Store
		escapedTx = tx
		if e := s.Store.WithWriteSession(func(*recovery.WriteSession) error { t.Fatal("offline exposed live session"); return nil }); e == nil {
			t.Fatal("offline-to-live bridge accepted")
		}
		if e := s.WithSession(nil).PurgeSource("scene", "root"); e == nil {
			t.Fatal("offline converted through WithSession")
		}
		return s.Store.WithMaintenanceScope(func(bound *recovery.MaintenanceTx) error {
			if bound != tx {
				t.Fatal("owner was rebound to another scope")
			}
			return bound.CheckScope(ctx)
		})
	}))
	if e := escapedTx.CheckScope(ctx); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired tx %v", e)
	}
	if _, e := escaped.reader().Head("commitment-1"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired store %v", e)
	}
	if e := escaped.WithMaintenanceScope(func(*recovery.MaintenanceTx) error { return nil }); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired owner bridge %v", e)
	}
	noNewOfflineIntents(t, f)
}
func TestOfflineNestedScopesPermitActiveParentAndExpireChild(t *testing.T) {
	f, conn, owner := offlineFixture(t)
	ctx := context.Background()
	var child *Store
	must(t, owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		s := boundOffline(t, f, owner, tx, ctx, f.service.owners)
		if e := s.Store.Transaction(ctx, func(c *Store) error {
			child = c
			if _, e := s.Store.reader().Head("commitment-1"); e != nil {
				return e
			}
			_, e := c.reader().Head("commitment-1")
			return e
		}); e != nil {
			return e
		}
		if _, e := child.reader().Head("commitment-1"); !errors.Is(e, sql.ErrTxDone) {
			t.Fatalf("completed child %v", e)
		}
		_, e := s.Store.reader().Head("commitment-1")
		return e
	}))
	if readerVersion(t, conn) != 1 {
		t.Fatal("scope-only reads upgraded compatibility")
	}
	noNewOfflineIntents(t, f)
}
func TestOfflineCaughtNestedFailureRetainsParentAndRollsBackReader(t *testing.T) {
	f, conn, owner := offlineFixture(t)
	ctx := context.Background()
	prior := stringify(t, offlineTables(t, conn))
	boom := errors.New("synthetic_afterWrite")
	must(t, owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		s := boundOffline(t, f, owner, tx, ctx, Owners{AfterWrite: func(*Store, *Object, []any, []any) error { return boom }})
		e := s.Store.Transaction(ctx, func(child *Store) error {
			nested, _ := NewPrivacyService(child, s.owners)
			return nested.PurgeSource("scene", "root")
		})
		if !errors.Is(e, boom) {
			t.Fatalf("child failure %v", e)
		}
		var raw string
		if e = tx.QueryRowContext(ctx, "SELECT value FROM recovery_gate").Scan(&raw); e != nil {
			return e
		}
		if get(parse(t, raw), "minimumReader") != float64(1) {
			t.Fatal("failed child's reader upgrade persisted")
		}
		_, e = s.Store.reader().Head("commitment-1")
		return e
	}))
	if stringify(t, offlineTables(t, conn)) != prior {
		t.Fatal("failed child mutation persisted")
	}
	noNewOfflineIntents(t, f)
}
func TestOfflineNativeRollbackRevokesRootEvenWhenCaught(t *testing.T) {
	f, conn, owner := offlineFixture(t)
	ctx := context.Background()
	_, e := conn.ExecContext(ctx, "CREATE TRIGGER offline_native_rollback BEFORE UPDATE ON memory_commitment_heads BEGIN SELECT RAISE(ROLLBACK,'offline_native_rollback'); END")
	must(t, e)
	var escaped *Store
	e = owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		s := boundOffline(t, f, owner, tx, ctx, f.service.owners)
		escaped = s.Store
		if e := s.PurgeSource("scene", "root"); e == nil {
			t.Fatal("native rollback hidden")
		}
		if e := tx.CheckScope(ctx); e == nil {
			t.Fatal("lost root stayed usable")
		}
		if e := s.Store.Clear("commitment-1"); e == nil {
			t.Fatal("lost root permitted autocommit")
		}
		return nil
	})
	if e == nil {
		t.Fatal("caught native loss allowed commit")
	}
	if e = escaped.Clear("commitment-1"); e == nil {
		t.Fatal("escaped lost adapter usable")
	}
	if readerVersion(t, conn) != 1 {
		t.Fatal("native rollback kept reader upgrade")
	}
	noNewOfflineIntents(t, f)
}
func TestOfflineCancellationAfterWriteRollsBackAndDoesNotJournal(t *testing.T) {
	f, conn, owner := offlineFixture(t)
	prior := stringify(t, offlineTables(t, conn))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options := f.store.options
	options.Now = func() string { t.Fatal("offline called clock"); return "" }
	port, e := NewOfflineMaintenance(owner, options, Owners{AfterWrite: func(*Store, *Object, []any, []any) error { cancel(); return nil }})
	must(t, e)
	e = port.Recall(ctx, "scene", "root")
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation %v", e)
	}
	if stringify(t, offlineTables(t, conn)) != prior {
		t.Fatal("canceled write persisted")
	}
	if readerVersion(t, conn) != 1 {
		t.Fatal("canceled upgrade persisted")
	}
	noNewOfflineIntents(t, f)
}
func TestOfflineAuthorityChangeRejectsOldAdmission(t *testing.T) {
	f, conn, owner := offlineFixture(t)
	ctx := context.Background()
	port, e := NewOfflineMaintenance(owner, f.store.options, f.service.owners)
	must(t, e)
	before := stringify(t, offlineTables(t, conn))
	head, e := f.ledger.Head(ctx)
	must(t, e)
	_, e = f.ledger.Append(ctx, recovery.Source{SceneID: "other", MessageID: "later"}, head, false)
	must(t, e)
	e = port.Recall(ctx, "scene", "root")
	if e == nil || !strings.Contains(e.Error(), "head_changed") {
		t.Fatalf("changed authority admission %v", e)
	}
	if stringify(t, offlineTables(t, conn)) != before {
		t.Fatal("stale admission mutated staging")
	}
	if readerVersion(t, conn) != 1 {
		t.Fatal("stale admission upgraded reader")
	}
}
func TestOfflineMarkerAndEraseRequireMaintenance(t *testing.T) {
	f := seeded(t)
	marker := obj("recordedAt", "2026-10-28T13:00:00.000Z", "head", get(sample(t), "head"), "reason", "purge")
	if e := f.service.recoverTemporalMarker(marker); e == nil {
		t.Fatal("live marker replay accepted")
	}
	if e := f.service.recoverErase("commitment-1", "scene", 1); e == nil {
		t.Fatal("live recovery erase accepted")
	}
}

func TestOfflineRequalifyDoesNotReadClockOrAppendMarker(t *testing.T) {
	f, conn, owner := offlineFixture(t)
	ctx := context.Background()
	must(t, owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		service := boundOffline(t, f, owner, tx, ctx, f.service.owners)
		service.Store.options.Now = func() string { t.Fatal("offline requalification read clock"); return "" }
		return service.Requalify(Context{SceneID: "scene"}, []Transition{{From: Reference{ID: "root", Version: 1}, To: Reference{ID: "root", Version: 2}}}, func(_ Context, evidence *Object) (*Candidate, error) {
			record := obj("id", "root", "scope", obj("kind", "scene", "sceneId", "scene", "personId", "a"), "content", "Synthetic declaration root", "kind", "fact", "epistemicKind", "explicit", "status", "active")
			return &Candidate{Ref: Reference{ID: "root", Version: 2}, View: &Assertion{Record: record, Proofs: arr(get(evidence, "proofs"))}}, nil
		})
	}))
	var count int
	must(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM memory_commitment_transitions").Scan(&count))
	if count != 1 {
		t.Fatal("offline requalification appended a clock marker")
	}
	if readerVersion(t, conn) != 10 {
		t.Fatal("existing temporal mutation did not require reader10")
	}
	noNewOfflineIntents(t, f)
}

func TestOfflineQueuedCancellationDoesNotWaitForActiveOwner(t *testing.T) {
	f, conn, owner := offlineFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	port, e := NewOfflineMaintenance(owner, f.store.options, Owners{AfterWrite: func(*Store, *Object, []any, []any) error { close(entered); <-release; return nil }})
	must(t, e)
	go func() { first <- port.Recall(context.Background(), "scene", "root") }()
	select {
	case <-entered:
	case e := <-first:
		t.Fatalf("first operation failed before owner: %v", e)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	// No deadline or timing threshold: cancellation must return while the first
	// callback is deliberately still blocked. A non-cancellable mutex deadlocks.
	e = port.Verify(canceled, nil)
	close(release)
	must(t, <-first)
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("queued cancellation %v", e)
	}
	if readerVersion(t, conn) != 10 {
		t.Fatal("first operation did not complete normally")
	}
	noNewOfflineIntents(t, f)
}
