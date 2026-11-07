package commitments

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/mattn/go-sqlite3"
)

func currentEnabled(Context) (bool, error) { return true, nil }
func currentFixture(t *testing.T) (*fixture, *assertions.Store, assertionauthority.Options) {
	t.Helper()
	f := seeded(t)
	ctx := context.Background()
	_, err := f.main.Exec("CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id)); CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL)")
	must(t, err)
	as, err := assertions.New(f.main, f.ledger, assertions.Options{DateParser: f.store.options.DateParser, Now: func() string { return f.now }})
	must(t, err)
	must(t, as.Initialize(ctx))
	opts := assertionauthority.Options{DateParser: f.store.options.DateParser, Compare: func(a, b string) (int, error) { return 0, errors.New("unexpected_observation_comparison") }, NowMillis: func() int64 { n, _, _ := f.store.options.DateParser.ParseMillis(f.now); return n }}
	co, err := assertions.NewCoordinator(as, assertions.CoordinatorOptions{HistorySupports: func(s *assertions.Store, id string) (proofs []any, err error) {
		err = s.Read(ctx, func(r *assertions.Reader) error {
			a, e := assertionauthority.New(r, opts)
			if e != nil {
				return e
			}
			proofs, e = a.HistorySupports(id)
			return e
		})
		return
	}, Retain: func(_ *assertions.Store, r *Object) (bool, error) { return true, nil }, RemoveProjection: func(*assertions.Store, string) error { return nil }, Committed: func(*assertions.Store, string, int64, int64) error { return nil }})
	must(t, err)
	proof := obj("sources", []any{obj("sceneId", "scene", "messageId", "root", "senderId", "a")}, "parents", []any{}, "parentReferences", []any{}, "subjectWitness", obj("kind", "self", "personId", "a", "sourceMessageId", "root"))
	record := obj("id", "root", "scope", obj("kind", "scene", "sceneId", "scene", "personId", "a"), "content", "Synthetic declaration root", "kind", "fact", "epistemicKind", "explicit", "sources", clone(get(proof, "sources")), "importance", "low", "confidence", float64(.5), "learnedAt", "2026-10-28T00:00:00.000Z", "status", "active", "locked", false, "origin", "learned", "lineageStatus", "verified")
	must(t, co.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		if e := tx.Repository.Write(record); e != nil {
			return e
		}
		return tx.Repository.SaveSupports("root", []any{proof})
	}))
	must(t, f.store.Transaction(ctx, func(s *Store) error {
		return as.WithSession(s.session).Read(ctx, func(r *assertions.Reader) error {
			authority, e := assertionauthority.New(r, opts)
			if e != nil {
				return e
			}
			view, e := authority.RelationAuthority(assertionauthority.Context{SceneID: "scene"}, Reference{ID: "root", Version: 1})
			if e != nil {
				return e
			}
			if view == nil {
				return errors.New("synthetic_real_authority_missing")
			}
			fingerprint, e := BackingFingerprint(view.Record)
			if e != nil {
				return e
			}
			h, e := s.reader().Head("commitment-1")
			if e != nil {
				return e
			}
			rev, e := s.reader().Revision("commitment-1", 1)
			if e != nil {
				return e
			}
			for _, raw := range arr(get(rev, "evidence")) {
				ev := object(raw)
				_ = ev.Set("proofs", clone(view.Proofs))
				_ = ev.Set("backingFingerprint", fingerprint)
			}
			return s.Put(h, rev)
		})
	}))
	return f, as, opts
}
func TestCurrentReadRealAuthoritySharedPublication(t *testing.T) {
	f, as, opts := currentFixture(t)
	ctx := context.Background()
	var held *ReadService
	before := rowsValue(t, f.main, "SELECT name FROM sqlite_master ORDER BY name")
	must(t, f.store.ReadCurrent(ctx, as, currentEnabled, opts, func(s *ReadService) error {
		held = s
		item, e := s.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
		if e != nil {
			return e
		}
		if get(item, "confirmation") != "confirmed" || len(arr(get(item, "evidence"))) != 2 {
			t.Fatalf("real authority not used: %s", stringify(t, item))
		}
		list, e := s.List(Context{SceneID: "scene"}, ReadOptions{})
		if e != nil {
			return e
		}
		if len(arr(get(list, "items"))) != 1 {
			t.Fatal("missing current plan")
		}
		return nil
	}))
	if _, e := held.Current(Context{SceneID: "scene"}, Reference{}); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired service: %v", e)
	}
	after := rowsValue(t, f.main, "SELECT name FROM sqlite_master ORDER BY name")
	if !deepEqual(before, after) {
		t.Fatal("read created schema")
	}
}
func TestCurrentReadRealRecallAndExpiry(t *testing.T) {
	for _, mutation := range []string{"recall", "expiry", "quarantine", "wrong-proof", "wrong-scene", "edit"} {
		t.Run(mutation, func(t *testing.T) {
			f, as, opts := currentFixture(t)
			switch mutation {
			case "recall":
				_, e := f.main.Exec("INSERT INTO recall_tombstones VALUES('scene','root','2026-10-28T13:00:00.000Z')")
				must(t, e)
			case "expiry", "quarantine", "wrong-scene":
				var raw string
				must(t, f.main.QueryRow("SELECT value FROM memories WHERE id='root'").Scan(&raw))
				r := object(parse(t, raw))
				if mutation == "expiry" {
					_ = r.Set("validUntil", "2026-10-28T12:59:59.000Z")
				}
				if mutation == "quarantine" {
					_ = r.Set("lineageStatus", "quarantined")
				}
				if mutation == "wrong-scene" {
					_ = object(get(r, "scope")).Set("sceneId", "other")
				}
				_, e := f.main.Exec("UPDATE memories SET value=? WHERE id='root'", stringify(t, r))
				must(t, e)
			case "wrong-proof":
				must(t, f.store.Transaction(context.Background(), func(s *Store) error {
					h, e := s.reader().Head("commitment-1")
					if e != nil {
						return e
					}
					r, e := s.reader().Revision("commitment-1", 1)
					if e != nil {
						return e
					}
					for _, ev := range arr(get(r, "evidence")) {
						for _, p := range arr(get(ev, "proofs")) {
							_ = object(p).Set("proofId", "wrong")
						}
					}
					return s.Put(h, r)
				}))
			case "edit":
				// A native registered edit makes a previously unversioned source ineligible.
				_, e := f.main.Exec("INSERT INTO scenes VALUES('scene',?)", stringify(t, obj("id", "scene", "adapterId", "adapter", "accountId", "account")))
				must(t, e)
				events, e := sourceevents.New(f.main, f.ledger, sourceevents.Options{ID: func() string { return "synthetic-event" }, Now: func() string { return f.now }})
				must(t, e)
				change := sourceevents.Change{Kind: "ingress", Identity: sourceevents.Identity{SceneID: "scene", AdapterID: "adapter", AccountID: "account", MessageID: "root", SenderID: "a"}, BodyHash: strings.Repeat("a", 64)}
				_, e = events.Observe(context.Background(), change)
				must(t, e)
				change.Kind = "edit"
				change.BodyHash = strings.Repeat("b", 64)
				change.EditID = "synthetic-edit"
				change.Order = sourceevents.Order{Kind: "platform_sequence", Sequence: 1}
				_, e = events.Observe(context.Background(), change)
				must(t, e)
			}
			must(t, f.store.ReadCurrent(context.Background(), as, currentEnabled, opts, func(s *ReadService) error {
				item, e := s.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
				if e != nil {
					return e
				}
				if has(item, "terms") || len(arr(get(item, "evidence"))) != 0 {
					t.Fatalf("unqualified evidence survived %s", stringify(t, item))
				}
				return nil
			}))
		})
	}
}
func TestCurrentReadWriteChildScopeAndFailure(t *testing.T) {
	f, as, opts := currentFixture(t)
	ctx := context.Background()
	sentinel := errors.New("synthetic_callback")
	err := f.store.Transaction(ctx, func(s *Store) error {
		return s.ReadCurrent(ctx, as, currentEnabled, opts, func(parent *ReadService) error {
			if err := s.Transaction(ctx, func(child *Store) error {
				if _, err := parent.List(Context{SceneID: "scene"}, ReadOptions{}); err == nil {
					t.Fatal("suspended parent accepted")
				}
				return child.ReadCurrent(ctx, as, currentEnabled, opts, func(active *ReadService) error {
					item, err := active.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
					if err != nil {
						return err
					}
					if get(item, "confirmation") != "confirmed" {
						t.Fatal("child missed real authority")
					}
					return nil
				})
			}); err != nil {
				return err
			}
			if _, err := parent.List(Context{SceneID: "scene"}, ReadOptions{}); err != nil {
				return err
			}
			return sentinel
		})
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("callback failure lost %v", err)
	}
	foreign := setup(t)
	other, e := assertions.New(foreign.main, foreign.ledger, assertions.Options{})
	must(t, e)
	if err = f.store.ReadCurrent(ctx, other, currentEnabled, opts, func(*ReadService) error { t.Fatal("foreign owner called"); return nil }); err == nil {
		t.Fatal("foreign owner accepted")
	}
}
func TestBorrowedReadersNativeReadOnlyAndExpiry(t *testing.T) {
	f, as, _ := currentFixture(t)
	var ar *assertions.Reader
	var cr *Reader
	must(t, f.ledger.WithWorkingSetRead(context.Background(), f.main, func(scope *recovery.WorkingSetReadSession) error {
		var err error
		ar, err = as.BorrowReader(scope.CheckScope, func(q string, args ...any) (assertions.Rows, error) { return scope.Query(q, args...) }, publicationPrepare(scope))
		if err != nil {
			return err
		}
		cr, err = BorrowReadScope(scope.CheckScope, scope.CheckScope, func(q string, args ...any) (Rows, error) { return scope.Query(q, args...) }, f.store.options)
		if err != nil {
			return err
		}
		if _, err = cr.Head("commitment-1"); err != nil {
			return err
		}
		if _, err = ar.Record("root"); err != nil {
			return err
		}
		for _, q := range []string{"DELETE FROM memory_commitment_heads RETURNING id", "CREATE TABLE forbidden(id)", "COMMIT", "PRAGMA writable_schema=ON"} {
			rows, e := scope.Query(q)
			if e == nil {
				rows.Close()
				t.Fatalf("native publication allowed %s", q)
			}
		}
		return nil
	}))
	if _, err := ar.Record("root"); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("assertion reader retained scope: %v", err)
	}
	if _, err := cr.Head("commitment-1"); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("commitment reader retained scope: %v", err)
	}
}

func TestCurrentReadDisabledNoInitializationAndDynamicPolicy(t *testing.T) {
	f := setup(t)
	as, e := assertions.New(f.main, f.ledger, assertions.Options{})
	must(t, e)
	opts := assertionauthority.Options{DateParser: f.store.options.DateParser, Compare: f.store.options.Compare}
	calls := 0
	enabled := false
	e = f.store.ReadCurrent(context.Background(), as, func(Context) (bool, error) { calls++; return enabled, nil }, opts, func(s *ReadService) error {
		_, err := s.Current(Context{SceneID: "scene", Simulation: true}, Reference{})
		if err != nil {
			return err
		}
		if calls != 0 {
			t.Fatal("simulation invoked flag")
		}
		invalid := ""
		_, err = s.List(Context{}, ReadOptions{Lifecycle: &invalid})
		if err == nil || err.Error() != "commitment_read_lifecycle" {
			t.Fatal("empty lifecycle accepted", err)
		}
		if calls != 0 {
			t.Fatal("invalid list invoked flag")
		}
		page, err := s.List(Context{SceneID: "scene"}, ReadOptions{})
		if err != nil {
			return err
		}
		if get(page, "coverage") != "complete" || len(arr(get(page, "items"))) != 0 {
			t.Fatal("disabled list not empty")
		}
		enabled = true
		_, err = s.Current(Context{SceneID: "scene"}, Reference{ID: "absent", Version: 1})
		if err == nil {
			t.Fatal("flag was cached or missing table hidden")
		}
		if calls != 2 {
			t.Fatalf("flag calls %d", calls)
		}
		return nil
	})
	must(t, e)
	var count int
	must(t, f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'memory_%'").Scan(&count))
	if count != 0 {
		t.Fatal("read initialized schemas")
	}
}
func TestBorrowedReadersKeepReadinessSeparate(t *testing.T) {
	f, _, opts := currentFixture(t)
	readyCalls := 0
	blocked := errors.New("synthetic_readiness")
	must(t, f.ledger.WithWorkingSetRead(context.Background(), f.main, func(scope *recovery.WorkingSetReadSession) error {
		ar, e := assertions.BorrowReadScope(scope.CheckScope, func() error { readyCalls++; return blocked }, func(q string, args ...any) (assertions.Rows, error) { return scope.Query(q, args...) }, publicationPrepare(scope))
		if e != nil {
			return e
		}
		cr, e := BorrowReadScope(scope.CheckScope, scope.CheckScope, func(q string, args ...any) (Rows, error) { return scope.Query(q, args...) }, f.store.options)
		if e != nil {
			return e
		}
		s, e := newReadService(cr, ar, currentEnabled, opts)
		if e != nil {
			return e
		}
		if readyCalls != 0 {
			t.Fatal("construction invoked readiness")
		}
		result, e := s.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
		if !errors.Is(e, blocked) || result != nil {
			t.Fatalf("partial current escaped: %v %v", result, e)
		}
		result, e = s.List(Context{SceneID: "scene"}, ReadOptions{})
		if !errors.Is(e, blocked) || result != nil {
			t.Fatalf("partial list escaped: %v %v", result, e)
		}
		return nil
	}))
}

func TestCurrentReadNativeTransactionLossCannotPublish(t *testing.T) {
	f, as, opts := currentFixture(t)
	ctx := context.Background()
	// Test-only engine fault injection. Production read capabilities expose no
	// driver connection or transaction-control operation. MaxOpenConns(1) makes
	// this the same native connection the publication borrows after Close.
	conn, e := f.main.Conn(ctx)
	must(t, e)
	var native *sqlite3.SQLiteConn
	must(t, conn.Raw(func(value any) error { native = value.(*sqlite3.SQLiteConn); return nil }))
	must(t, conn.Close())
	var held *ReadService
	e = f.store.ReadCurrent(ctx, as, currentEnabled, opts, func(s *ReadService) error {
		held = s
		item, e := s.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
		if e != nil {
			return e
		}
		if item == nil {
			t.Fatal("initial snapshot missing")
		}
		native.RegisterAuthorizer(nil)
		if _, e = native.Exec("ROLLBACK", nil); e != nil {
			return e
		}
		// A genuine SQLite query failure is the boundary at which the native scope
		// latches loss. Catching the error must not revive an autocommit snapshot.
		if _, e = s.reader.query("SELECT * FROM main.synthetic_missing_after_loss"); !errors.Is(e, sql.ErrTxDone) {
			t.Fatalf("native loss not detected %v", e)
		}
		if item, e = s.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1}); !errors.Is(e, sql.ErrTxDone) || item != nil {
			t.Fatalf("lost snapshot returned plan %v %v", item, e)
		}
		return nil
	})
	if e == nil {
		t.Fatal("caught native loss published success")
	}
	if _, e = held.List(Context{SceneID: "scene"}, ReadOptions{}); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("native capability revived %v", e)
	}
}

func TestCurrentReadRejectsAssertionSessionWithoutMatchingCommitmentSession(t *testing.T) {
	f, as, opts := currentFixture(t)
	must(t, f.store.Transaction(context.Background(), func(bound *Store) error {
		scoped := as.WithSession(bound.session)
		// A single-connection pool would self-block if ReadCurrent entered Publish.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		e := f.store.ReadCurrent(ctx, scoped, currentEnabled, opts, func(*ReadService) error { t.Fatal("mismatched session reached callback"); return nil })
		if e == nil || e.Error() != "assertion_store_session_binding" {
			t.Fatalf("binding error %v", e)
		}
		return bound.ReadCurrent(ctx, scoped, currentEnabled, opts, func(s *ReadService) error {
			_, e := s.Current(Context{SceneID: "scene"}, Reference{ID: "commitment-1", Version: 1})
			return e
		})
	}))
}

func TestBorrowedSupportReadersUseNativePreparedQueries(t *testing.T) {
	f, as, _ := currentFixture(t)
	counts := map[string]int{}
	var held *assertions.Reader
	must(t, f.ledger.WithWorkingSetRead(context.Background(), f.main, func(scope *recovery.WorkingSetReadSession) error {
		native := publicationPrepare(scope)
		ar, e := as.BorrowReader(scope.CheckScope, func(q string, args ...any) (assertions.Rows, error) { return scope.Query(q, args...) }, func(q string) (assertions.PreparedRead, error) { counts[q]++; return native(q) })
		if e != nil {
			return e
		}
		return ar.WithSupportReadStatements(func(r *assertions.Reader) error {
			held = r
			for i := 0; i < 3; i++ {
				record, e := r.Record("root")
				if e != nil {
					return e
				}
				if record == nil {
					t.Fatal("missing record")
				}
				if _, e = r.Supports("root"); e != nil {
					return e
				}
			}
			return nil
		})
	}))
	if len(counts) != 2 {
		t.Fatalf("expected two native prepares, got %v", counts)
	}
	for q, n := range counts {
		if n != 1 {
			t.Fatalf("recompiled %q: %d", q, n)
		}
	}
	if _, e := held.Record("root"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("prepared reader escaped", e)
	}
}
