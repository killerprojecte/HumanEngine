package projections

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type fixture struct {
	db        *sql.DB
	path      string
	authority *recovery.Authority
	store     *assertions.Store
	outbox    *Outbox
	now       float64
	options   assertionauthority.Options
}

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	must(t, os.Mkdir(authorityPath, 0700))
	ledger, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	must(t, e)
	_, e = ledger.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	must(t, e)
	must(t, ledger.Close())
	must(t, os.Mkdir(filepath.Join(root, "live"), 0700))
	path := filepath.Join(root, "live", "main.sqlite")
	db, e := sql.Open("sqlite3", path+"?_foreign_keys=on")
	must(t, e)
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id))`)
	must(t, e)
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: 1, State: "ready"}
	raw, e := json.Marshal(gate)
	must(t, e)
	_, e = db.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw))
	must(t, e)
	a, e := recovery.OpenWriter(ctx, authorityPath)
	must(t, e)
	parser, e := jsdate.New("UTC")
	must(t, e)
	store, e := assertions.New(db, a, assertions.Options{DateParser: parser})
	must(t, e)
	must(t, store.Initialize(ctx))
	f := &fixture{db: db, path: path, authority: a, store: store, now: 1000, options: assertionauthority.Options{DateParser: parser, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }}}
	f.outbox, e = NewOutbox(ctx, db, a, Options{Now: func() float64 { return f.now }})
	must(t, e)
	t.Cleanup(func() { _ = f.db.Close(); _ = a.Close() })
	return f
}
func (f *fixture) state(t *testing.T, id string, version, epoch int64, deleted bool) {
	t.Helper()
	_, e := f.db.Exec(`INSERT INTO memory_assertion_state VALUES(?,?,?,?) ON CONFLICT(assertion_id) DO UPDATE SET version=excluded.version,deletion_epoch=excluded.deletion_epoch,deleted=excluded.deleted`, id, version, epoch, deleted)
	must(t, e)
}
func (f *fixture) enqueue(t *testing.T, s Snapshot) int64 {
	t.Helper()
	var id int64
	must(t, f.outbox.Transaction(context.Background(), func(b *Outbox) error { var e error; id, e = b.Enqueue(s); return e }))
	return id
}
func (f *fixture) claim(t *testing.T) Lease {
	t.Helper()
	l, e := f.outbox.Claim(context.Background(), 100, 1)
	must(t, e)
	if len(l) != 1 {
		t.Fatalf("leases: %+v", l)
	}
	return l[0]
}
func (f *fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	must(t, f.db.QueryRow(q, args...).Scan(&n))
	return n
}

func TestRealFTSGuardedRootChildReopen(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p, e := NewProcessor(ctx, f.outbox, AssertionReader(f.store, f.options), nil)
	must(t, e)
	must(t, f.outbox.Transaction(ctx, func(root *Outbox) error {
		if _, e := root.exec("INSERT INTO memory_fts VALUES('a','scene:s',NULL,'first canary')"); e != nil {
			return e
		}
		return root.Transaction(ctx, func(child *Outbox) error {
			var id string
			found, e := child.one("SELECT assertion_id FROM memory_fts WHERE memory_fts MATCH ?", []any{"canary"}, &id)
			if e != nil {
				return e
			}
			if !found || id != "a" {
				t.Fatal(found, id)
			}
			if _, e = root.exec("DELETE FROM memory_fts"); e == nil || !strings.Contains(e.Error(), "scope_inactive") {
				t.Fatal("ancestor allowed", e)
			}
			if _, e = child.exec("CREATE TABLE forbidden(id)"); e == nil {
				t.Fatal("caller DDL allowed")
			}
			if _, e = child.exec("DELETE FROM memory_fts WHERE assertion_id='a'"); e != nil {
				return e
			}
			_, e = child.exec("INSERT INTO memory_fts VALUES('a','scene:s',NULL,'second replacement')")
			return e
		})
	}))
	if n := f.count(t, "SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'canary'"); n != 0 {
		t.Fatal(n)
	}
	must(t, f.db.Close())
	f.db, e = sql.Open("sqlite3", f.path+"?_foreign_keys=on")
	must(t, e)
	f.db.SetMaxOpenConns(1)
	f.outbox.main = f.db
	p.outbox = f.outbox
	must(t, f.outbox.Transaction(ctx, func(root *Outbox) error {
		return root.Transaction(ctx, func(child *Outbox) error {
			var n int
			_, e := child.one("SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'replacement'", nil, &n)
			if e != nil {
				return e
			}
			if n != 1 {
				t.Fatal(n)
			}
			return p.WithSession(child.session).Remove("a")
		})
	}))
	if n := f.count(t, "SELECT count(*) FROM memory_fts"); n != 0 {
		t.Fatal(n)
	}
}
func TestOutboxDuplicateClockAndStaleExpiryPrecedence(t *testing.T) {
	f := setup(t)
	s := Snapshot{"a", 1, 0, Lexical}
	f.state(t, "a", 1, 0, false)
	id := f.enqueue(t, s)
	lease := f.claim(t)
	f.now = math.NaN()
	if got := f.enqueue(t, s); got != id {
		t.Fatal(got, id)
	}
	f.state(t, "a", 2, 0, false)
	must(t, f.outbox.Transaction(context.Background(), func(b *Outbox) error {
		_, e := b.Enqueue(s)
		if e == nil || e.Error() != "projection_snapshot_stale" {
			t.Fatal(e)
		}
		return nil
	}))
	f.now = 1100
	leases, e := f.outbox.Claim(context.Background(), 100, 1)
	must(t, e)
	if len(leases) != 0 {
		t.Fatal(leases)
	}
	var reason string
	must(t, f.db.QueryRow("SELECT reason FROM memory_projection_outbox WHERE id=?", id).Scan(&reason))
	if reason != "snapshot_stale" {
		t.Fatal(reason)
	}
	called := false
	ok, e := f.outbox.Complete(context.Background(), lease, func(*Outbox) error { called = true; return nil })
	must(t, e)
	if ok || called {
		t.Fatal(ok, called)
	}
}
func TestCompleteCaughtRollbackAndLeaseBoundary(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.state(t, "a", 1, 0, false)
	f.enqueue(t, Snapshot{"a", 1, 0, Lexical})
	l := f.claim(t)
	must(t, f.outbox.Transaction(ctx, func(parent *Outbox) error {
		_, e := parent.Complete(ctx, l, func(child *Outbox) error {
			_, e := child.exec("UPDATE memory_assertion_state SET version=2 WHERE assertion_id='a'")
			return e
		})
		if e == nil || !strings.Contains(e.Error(), "projection_changed_during_commit") {
			t.Fatal(e)
		}
		var v int
		_, e = parent.one("SELECT version FROM memory_assertion_state WHERE assertion_id='a'", nil, &v)
		if e != nil {
			return e
		}
		if v != 1 {
			t.Fatal(v)
		}
		_, e = parent.Complete(ctx, l, func(child *Outbox) error {
			_, e := child.exec("UPDATE memory_assertion_state SET deletion_epoch=9 WHERE assertion_id='a'")
			if e != nil {
				return e
			}
			return errors.New("synthetic_callback")
		})
		if e == nil || !strings.Contains(e.Error(), "synthetic_callback") {
			t.Fatal(e)
		}
		f.now = 1100
		ok, e := parent.Complete(ctx, l, func(*Outbox) error { t.Fatal("expired callback ran"); return nil })
		if e != nil {
			return e
		}
		if ok {
			t.Fatal(ok)
		}
		return nil
	}))
	if n := f.count(t, "SELECT deletion_epoch FROM memory_assertion_state WHERE assertion_id='a'"); n != 0 {
		t.Fatal(n)
	}
	replacement := f.claim(t)
	if replacement.Attempts != 2 || replacement.LeaseToken == l.LeaseToken {
		t.Fatal(replacement)
	}
}
func TestRetryAndPruneRetention(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.state(t, "a", 1, 0, false)
	f.enqueue(t, Snapshot{"a", 1, 0, Lexical})
	for attempt := int64(1); attempt <= 3; attempt++ {
		l := f.claim(t)
		if l.Attempts != attempt {
			t.Fatal(l)
		}
		ok, e := f.outbox.Fail(ctx, l, "projection_failed")
		must(t, e)
		if !ok {
			t.Fatal("not owned")
		}
		f.now += 1000 * math.Pow(2, float64(attempt-1))
	}
	if n := f.count(t, "SELECT count(*) FROM memory_projection_outbox WHERE state='failed'"); n != 1 {
		t.Fatal(n)
	}
	must(t, f.outbox.Transaction(ctx, func(b *Outbox) error {
		for i := 0; i < 4100; i++ {
			if _, e := b.exec(`INSERT INTO memory_projection_outbox(assertion_id,version,deletion_epoch,kind,state,available_at,updated_at) VALUES(?,1,0,'lexical','done',0,0)`, fmt.Sprintf("obsolete-%d", i)); e != nil {
				return e
			}
		}
		_, e := b.exec(`INSERT INTO memory_projection_outbox(assertion_id,version,deletion_epoch,kind,state,available_at,updated_at) VALUES('inflight',1,0,'lexical','pending',0,0)`)
		return e
	}))
	n, e := f.outbox.PruneTerminal(ctx, 500)
	must(t, e)
	if n != 4 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT count(*) FROM memory_projection_outbox"); n != 4098 {
		t.Fatal(n)
	}
	if got := f.enqueue(t, Snapshot{"a", 1, 0, Lexical}); got != 1 {
		t.Fatal(got)
	}
}
func TestProductionEligibilityFutureAndDisabledPrivacy(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enabled := false
	c, e := New(ctx, f.db, f.authority, func() bool { return enabled }, AssertionReader(f.store, f.options), Options{Now: func() float64 { return f.now }})
	must(t, e)
	f.state(t, "future", 1, 0, false)
	f.state(t, "internal", 1, 0, false)
	for _, id := range []string{"future", "internal"} {
		raw := fmt.Sprintf(`{"id":%q,"scope":{"kind":"scene","sceneId":"s","personId":"p"},"content":"futurecanary","status":"active","origin":"manual","validFrom":"2999-01-01T00:00:00.000Z","internalUse":%t}`, id, id == "internal")
		_, e = f.db.Exec("INSERT INTO memories VALUES(?, 'scene:s','p','futurecanary',?,?)", id, id, raw)
		must(t, e)
	}
	// Disabled construction/flush did not backfill either record.
	result, e := c.Flush(ctx)
	must(t, e)
	if result != nil || f.count(t, "SELECT count(*) FROM memory_projection_outbox") != 0 {
		t.Fatal(result)
	}
	enabled = true
	must(t, f.store.Transaction(ctx, func(s *assertions.Store) error {
		if e := c.CommittedFrom(s, "future", 1, 0); e != nil {
			return e
		}
		return c.CommittedFrom(s, "internal", 1, 0)
	}))
	result, e = c.Flush(ctx)
	must(t, e)
	if *result != (DrainResult{Claimed: 4, Completed: 2, Invalid: 2}) {
		t.Fatal(result)
	}
	if n := f.count(t, "SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'futurecanary'"); n != 1 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT count(*) FROM memory_person_projection_state WHERE assertion_id='future' AND person_id='p'"); n != 1 {
		t.Fatal(n)
	}
	enabled = false
	must(t, f.store.Transaction(ctx, func(s *assertions.Store) error { return c.RemoveFrom(s, "future") }))
	if n := f.count(t, "SELECT count(*) FROM memory_fts"); n != 0 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT count(*) FROM memory_person_projection_state"); n != 0 {
		t.Fatal(n)
	}
}

func TestNativeRollbackRevokesRootAndCannotAutocommit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.state(t, "a", 1, 0, false)
	f.enqueue(t, Snapshot{"a", 1, 0, Lexical})
	l := f.claim(t)
	_, e := f.db.Exec(`CREATE TABLE tripwire(id); CREATE TRIGGER force_rollback BEFORE INSERT ON tripwire BEGIN SELECT RAISE(ROLLBACK,'synthetic_native_rollback'); END`)
	must(t, e)
	e = f.outbox.Transaction(ctx, func(root *Outbox) error {
		_, err := root.Complete(ctx, l, func(child *Outbox) error {
			_, native := child.exec("INSERT INTO tripwire VALUES(1)")
			if native == nil {
				t.Fatal("native rollback missing")
			}
			if _, next := child.exec("UPDATE memory_assertion_state SET version=2"); next == nil {
				t.Fatal("lost child autocommitted")
			}
			return nil // Catching the native error cannot revive the root.
		})
		if err == nil {
			t.Fatal("complete accepted native transaction loss")
		}
		if _, next := root.exec("UPDATE memory_assertion_state SET version=3"); next == nil {
			t.Fatal("lost root autocommitted")
		}
		return nil
	})
	if e == nil || !strings.Contains(e.Error(), "recovery_main_transaction_lost") {
		t.Fatal(e)
	}
	if n := f.count(t, "SELECT version FROM memory_assertion_state WHERE assertion_id='a'"); n != 1 {
		t.Fatal(n)
	}
}

func TestCompletionCallbackExpiryRollsBackAndScopedReader(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.state(t, "a", 1, 0, false)
	f.enqueue(t, Snapshot{"a", 1, 0, Lexical})
	l := f.claim(t)
	must(t, f.outbox.Transaction(ctx, func(root *Outbox) error {
		suspended := f.store.WithSession(root.session)
		_, e := root.Complete(ctx, l, func(child *Outbox) error {
			if err := suspended.Read(ctx, func(r *assertions.Reader) error { return r.CheckScope() }); err == nil {
				t.Fatal("suspended assertion store accepted")
			}
			if err := f.store.WithSession(child.session).Read(ctx, func(r *assertions.Reader) error { return r.CheckScope() }); err != nil {
				return err
			}
			if _, err := child.exec("UPDATE memory_assertion_state SET deletion_epoch=5"); err != nil {
				return err
			}
			// Restore the snapshot so only expiry revokes this completion.
			if _, err := child.exec("UPDATE memory_assertion_state SET deletion_epoch=0"); err != nil {
				return err
			}
			f.now = 1100
			return nil
		})
		if e == nil || !strings.Contains(e.Error(), "projection_changed_during_commit") {
			t.Fatal(e)
		}
		return nil
	}))
	if n := f.count(t, "SELECT count(*) FROM memory_projection_outbox WHERE state='leased'"); n != 1 {
		t.Fatal(n)
	}
}

func TestDrainOperationalFailureAndUnavailableCounters(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, id := range []string{"unavailable", "broken"} {
		f.state(t, id, 1, 0, false)
		f.enqueue(t, Snapshot{id, 1, 0, Lexical})
	}
	p, e := NewProcessor(ctx, f.outbox, func(s *recovery.WriteSession, id string) (*Object, error) {
		if id == "broken" {
			return nil, errors.New("synthetic private canary not persisted")
		}
		return nil, nil
	}, nil)
	must(t, e)
	result, e := p.Drain(ctx, 16)
	must(t, e)
	if result != (DrainResult{Claimed: 2, Invalid: 1, Failed: 1}) {
		t.Fatal(result)
	}
	if n := f.count(t, "SELECT count(*) FROM memory_projection_outbox WHERE reason LIKE '%canary%'"); n != 0 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT count(*) FROM memory_projection_outbox WHERE state='pending' AND available_at=2000 AND reason='projection_failed'"); n != 1 {
		t.Fatal(n)
	}
}

func TestCompletionCancellationCannotReviveCapability(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.state(t, "a", 1, 0, false)
	f.enqueue(t, Snapshot{"a", 1, 0, Lexical})
	l := f.claim(t)
	_, e := f.outbox.Complete(ctx, l, func(child *Outbox) error {
		if _, e := child.exec("UPDATE memory_assertion_state SET version=2"); e != nil {
			return e
		}
		cancel()
		if _, e := child.exec("UPDATE memory_assertion_state SET version=3"); e == nil {
			t.Fatal("canceled capability reused")
		}
		return nil
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if n := f.count(t, "SELECT version FROM memory_assertion_state WHERE assertion_id='a'"); n != 1 {
		t.Fatal(n)
	}
}

// A real support reader must see evidence withdrawal on its current connection,
// not a cached eligibility result or another connection's committed snapshot.
func TestProductionEligibilitySeesSameScopeEvidenceRecall(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.state(t, "supported", 1, 0, false)
	_, e := f.db.Exec(`INSERT INTO memories VALUES('supported','scene:s','p','proofcanary','proofcanary','{"id":"supported","scope":{"kind":"scene","sceneId":"s","personId":"p"},"content":"proofcanary","status":"active","lineageStatus":"verified"}'); INSERT INTO memory_supports VALUES('supported','[{"sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"parents":[]}]')`)
	must(t, e)
	eligible := AssertionReader(f.store, f.options)
	c, e := New(ctx, f.db, f.authority, func() bool { return true }, eligible, Options{Now: func() float64 { return f.now }})
	must(t, e)
	must(t, f.outbox.Transaction(ctx, func(root *Outbox) error {
		return root.Transaction(ctx, func(child *Outbox) error {
			record, e := eligible(child.session, "supported")
			if e != nil {
				return e
			}
			if record == nil {
				t.Fatal("initial live proof rejected")
			}
			if e = c.WithSession(child.session).Committed("supported", 1, 0); e != nil {
				return e
			}
			if _, e = child.exec("INSERT INTO recall_tombstones VALUES('s','m','2026-10-28T12:00:00.000Z')"); e != nil {
				return e
			}
			record, e = eligible(child.session, "supported")
			if e != nil {
				return e
			}
			if record != nil {
				t.Fatal("same-scope recalled proof remained eligible")
			}
			return nil
		})
	}))
	result, e := c.Flush(ctx)
	must(t, e)
	if *result != (DrainResult{Claimed: 2, Invalid: 2}) {
		t.Fatal(result)
	}
	if f.count(t, "SELECT count(*) FROM memory_fts") != 0 || f.count(t, "SELECT count(*) FROM memory_person_projection_state") != 0 {
		t.Fatal("withdrawn evidence projected")
	}
	if f.count(t, "SELECT count(*) FROM memory_projection_outbox WHERE state='invalid' AND reason='source_unavailable'") != 2 {
		t.Fatal("withdrawal result not retained")
	}
}
