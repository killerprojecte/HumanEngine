package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	main       *sql.DB
	ledger     *recovery.Authority
	assertions *assertions.Store
	storage    *Store
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	if e := os.Mkdir(authorityPath, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(root, "live"), 0700); e != nil {
		t.Fatal(e)
	}
	main, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite")+"?_foreign_keys=on")
	if e != nil {
		t.Fatal(e)
	}
	main.SetMaxOpenConns(1)
	_, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE scenes(id TEXT PRIMARY KEY,value TEXT NOT NULL);CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id));INSERT INTO scenes VALUES('s','{"id":"s","adapterId":"adapter","accountId":"account"}')`)
	if e != nil {
		t.Fatal(e)
	}
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: 1, State: "ready"}
	raw, _ := json.Marshal(gate)
	if _, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	ra, e := recovery.OpenWriter(ctx, authorityPath)
	if e != nil {
		t.Fatal(e)
	}
	store, e := assertions.New(main, ra, assertions.Options{})
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	knowledge, e := NewStore(main, ra)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ra.Close(); main.Close() })

	return &fixture{main: main, ledger: ra, assertions: store, storage: knowledge}
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func parse(t *testing.T, raw string) any { t.Helper(); v, e := jsjson.Parse(raw); must(t, e); return v }
func jsonText(t *testing.T, v any) string {
	t.Helper()
	s, e := jsjson.Stringify(v)
	must(t, e)
	return s
}
func TestOptionalStorageAndBarrierWithoutIntent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var retained *Store
	must(t, f.storage.Transaction(ctx, func(s *Store) error {
		retained = s
		rows, e := s.Rows(nil, nil)
		if e != nil {
			return e
		}
		if len(rows) != 0 {
			t.Fatal(rows)
		}
		return s.Remove("missing")
	}))
	var n int
	must(t, f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='memory_knowledge_edges'").Scan(&n))
	if n != 0 {
		t.Fatal("read created table")
	}
	if e := retained.Remove("expired"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal("expired", e)
	}
	edge := testEdge("a", testProof("p", "deleted"), testProof("q"))
	must(t, f.storage.Transaction(ctx, func(s *Store) error { return s.Put(edge, "preserved-key") }))
	gate, e := recovery.ReadGate(ctx, f.main)
	must(t, e)
	if gate.MinimumReader != 7 || gate.Head.Sequence != 0 {
		t.Fatal(gate)
	}
	must(t, f.storage.Transaction(ctx, func(s *Store) error {
		if e := NewLifecycle(s).PurgeAssertion("deleted"); e != nil {
			return e
		}
		v, e := s.ByKey("preserved-key")
		if e == nil && (v == nil || get(v, "version") != float64(2)) {
			t.Fatal("lost key/version", v)
		}
		return e
	}))
	gate, e = recovery.ReadGate(ctx, f.main)
	must(t, e)
	if gate.Head.Sequence != 0 {
		t.Fatal("invented knowledge intent", gate)
	}
}
func TestStorageRollbackAndExactAssertionChild(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	edge := testEdge("a", testProof("q"))
	must(t, f.storage.Transaction(ctx, func(s *Store) error { return s.Put(edge, "key") }))
	service, e := New(f.main, f.ledger)
	must(t, e)
	sentinel := errors.New("rollback")
	e = f.assertions.Transaction(ctx, func(store *assertions.Store) error {
		if e := service.PurgeInAssertionScope(store, "backing-a"); e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	must(t, f.storage.Transaction(ctx, func(s *Store) error {
		v, e := s.Get("a")
		if v == nil {
			t.Fatal("purge escaped rollback")
		}
		return e
	}))
	must(t, f.assertions.Transaction(ctx, func(store *assertions.Store) error { return service.PurgeInAssertionScope(store, "backing-a") }))
	must(t, f.storage.Transaction(ctx, func(s *Store) error {
		v, e := s.Get("a")
		if v != nil {
			t.Fatal("not purged")
		}
		return e
	}))
}
func TestCorruptRowsFailBeforePurge(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	must(t, f.storage.Transaction(ctx, func(s *Store) error { return s.Put(testEdge("a", testProof("q")), "key") }))
	_, e := f.main.Exec("UPDATE memory_knowledge_edges SET assertion_id='mismatch'")
	must(t, e)
	e = f.storage.Transaction(ctx, func(s *Store) error { return NewLifecycle(s).PurgeAssertion("backing-a") })
	if e == nil || !strings.Contains(e.Error(), "knowledge_storage_corrupt") {
		t.Fatal(e)
	}
	var n int
	must(t, f.main.QueryRow("SELECT count(*) FROM memory_knowledge_edges").Scan(&n))
	if n != 1 {
		t.Fatal(n)
	}
}

func TestNestedKnowledgeScopeSuspensionAndRollback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	must(t, f.storage.Transaction(ctx, func(s *Store) error { return s.Put(testEdge("a", testProof("q")), "key") }))
	var escaped *Store
	must(t, f.storage.Transaction(ctx, func(parent *Store) error {
		stop := errors.New("child rollback")
		e := parent.Transaction(ctx, func(child *Store) error {
			escaped = child
			if _, e := parent.Get("a"); e == nil {
				t.Fatal("suspended parent accepted")
			}
			if e := child.Remove("a"); e != nil {
				return e
			}
			return stop
		})
		if !errors.Is(e, stop) {
			t.Fatalf("child result: %v", e)
		}
		row, e := parent.Get("a")
		if e == nil && row == nil {
			t.Fatal("child purge escaped rollback")
		}
		if _, e := escaped.Get("a"); e == nil {
			t.Fatal("expired child accepted")
		}
		return e
	}))
}

func TestKnowledgeReadinessFailureDoesNotMutate(t *testing.T) {
	for _, change := range []string{
		`UPDATE recovery_gate SET value=json_set(value,'$.state','recovering')`,
		`UPDATE recovery_gate SET value=json_set(value,'$.head.hash','ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff')`,
	} {
		t.Run(change, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			must(t, f.storage.Transaction(ctx, func(s *Store) error { return s.Put(testEdge("a", testProof("q")), "key") }))
			var original string
			must(t, f.main.QueryRow("SELECT value FROM recovery_gate WHERE id=1").Scan(&original))
			_, e := f.main.Exec(change)
			must(t, e)
			e = f.storage.Transaction(ctx, func(bound *Store) error { return bound.Remove("a") })
			if e == nil {
				t.Fatal("changed gate accepted")
			}
			_, e = f.main.Exec("UPDATE recovery_gate SET value=? WHERE id=1", original)
			must(t, e)
			must(t, f.storage.Transaction(ctx, func(s *Store) error {
				row, e := s.Get("a")
				if e == nil && row == nil {
					t.Fatal("failed readiness mutated row")
				}
				return e
			}))
		})
	}
}
