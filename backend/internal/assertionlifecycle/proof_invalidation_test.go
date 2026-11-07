package assertionlifecycle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
)

type fixture struct {
	main          *sql.DB
	ledger        *recovery.Authority
	authorityPath string
	store         *assertions.Store
	coordinator   *assertions.Coordinator
	options       assertionauthority.Options
	owners        Owners
	events        []any
	fault         string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	authorityPath := filepath.Join(root, "authority")
	must(t, os.Mkdir(authorityPath, 0700))
	authorityDB, e := sql.Open("sqlite3", filepath.Join(authorityPath, "authority.sqlite"))
	must(t, e)
	_, e = authorityDB.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	must(t, e)
	must(t, authorityDB.Close())
	must(t, os.Mkdir(filepath.Join(root, "live"), 0700))
	main, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite")+"?_foreign_keys=on")
	must(t, e)
	main.SetMaxOpenConns(1)
	_, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);CREATE TABLE recall_tombstones(scene_id TEXT NOT NULL,message_id TEXT NOT NULL,recalled_at TEXT NOT NULL,PRIMARY KEY(scene_id,message_id))`)
	must(t, e)
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: authorityPath, MinimumReader: 1, State: "ready"}
	raw, e := json.Marshal(gate)
	must(t, e)
	_, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw))
	must(t, e)
	ledger, e := recovery.OpenWriter(ctx, authorityPath)
	must(t, e)
	parser, e := jsdate.New("UTC")
	must(t, e)
	store, e := assertions.New(main, ledger, assertions.Options{DateParser: parser, Now: func() string { return "2026-10-28T00:00:00.000Z" }})
	must(t, e)
	must(t, store.Initialize(ctx))
	f := &fixture{main: main, ledger: ledger, authorityPath: authorityPath, store: store, events: []any{}, options: assertionauthority.Options{DateParser: parser, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }}}
	f.owners = Owners{
		RemoveProjection:       func(_ *assertions.Store, id string) error { return f.event("projection", id) },
		BeforePurgeHistory:     func(_ *assertions.Store, id string) error { return f.event("beforePurge", id) },
		BeforeInvalidateRecord: func(_ *assertions.Store, id string) error { return f.event("beforeInvalidate", id) },
	}
	f.coordinator, e = assertions.NewCoordinator(store, assertions.CoordinatorOptions{
		HistorySupports: func(s *assertions.Store, id string) (proofs []any, err error) {
			err = s.Read(ctx, func(r *assertions.Reader) error {
				a, e := assertionauthority.New(r, f.options)
				if e != nil {
					return e
				}
				proofs, e = a.HistorySupports(id)
				return e
			})
			return
		},
		Retain: func(s *assertions.Store, r *Object) (retain bool, err error) {
			if text(r, "status") == "invalidated" || text(r, "lineageStatus") == "quarantined" {
				return false, nil
			}
			retain = true
			err = s.Read(ctx, func(reader *assertions.Reader) error {
				v, _ := r.Get("sources")
				for _, v := range v.([]any) {
					source := v.(*Object)
					recalled, e := reader.Recalled(text(source, "sceneId"), text(source, "messageId"))
					if e != nil {
						return e
					}
					if recalled {
						retain = false
						break
					}
				}
				return nil
			})
			return
		},
		RemoveProjection: func(_ *assertions.Store, id string) error { return f.event("repositoryProjection", id) },
		Committed: func(_ *assertions.Store, id string, version, epoch int64) error {
			f.events = append(f.events, []any{"committed", id, float64(version), float64(epoch)})
			return nil
		},
	})
	must(t, e)
	t.Cleanup(func() { ledger.Close(); main.Close() })
	return f
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) event(kind, id string) error {
	f.events = append(f.events, []any{kind, id})
	if f.fault == kind {
		return errors.New("synthetic_" + kind)
	}
	return nil
}
func parse(t *testing.T, raw string) any { t.Helper(); v, e := jsjson.Parse(raw); must(t, e); return v }
func stringify(t *testing.T, v any) string {
	t.Helper()
	raw, e := jsjson.Stringify(v)
	must(t, e)
	return raw
}
func record(t *testing.T, id string) *Object {
	return parse(t, fmt.Sprintf(`{"id":%q,"scope":{"kind":"scene","sceneId":"scene","personId":"person"},"content":%q,"kind":"claim","sources":[{"sceneId":"scene","messageId":%q,"senderId":"person"}],"importance":"medium","confidence":0.6,"learnedAt":"2026-01-02T00:00:00.000Z","status":"active","locked":false,"origin":"learned","lineageStatus":"verified"}`, id, "Synthetic "+id, id)).(*Object)
}
func proof(t *testing.T, id string, parents ...string) *Object {
	p := parse(t, fmt.Sprintf(`{"sources":[{"sceneId":"scene","messageId":%q,"senderId":"person"}],"parents":[]}`, id)).(*Object)
	ids := []any{}
	for _, id := range parents {
		ids = append(ids, id)
	}
	must(t, p.Set("parents", ids))
	return p
}
func (f *fixture) seed(t *testing.T, ids ...string) {
	t.Helper()
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		for _, id := range ids {
			if e := tx.Repository.Write(record(t, id)); e != nil {
				return e
			}
			if e := tx.Repository.SaveSupports(id, []any{proof(t, id)}); e != nil {
				return e
			}
		}
		return nil
	}))
	f.events = []any{}
}
func (f *fixture) run(t *testing.T, tx *assertions.RevisionTxn, ops []any, results *[]any) error {
	t.Helper()
	p, e := New(tx, f.owners, f.options)
	if e != nil {
		return e
	}
	seen := IDSet{}
	for _, value := range ops {
		op := value.([]any)
		method := op[0].(string)
		args := op[1:]
		switch method {
		case "put":
			r := args[0].(*Object)
			if e = tx.Repository.Write(r); e == nil {
				e = tx.Repository.SaveSupports(text(r, "id"), args[1].([]any))
			}
		case "supports":
			e = tx.Repository.SaveSupports(args[0].(string), args[1].([]any))
		case "raw":
			e = tx.Store.WithWriteSession(func(s *recovery.WriteSession) error { _, e := s.Exec(args[0].(string)); return e })
		case "fault":
			f.fault = args[0].(string)
		case "throw":
			e = errors.New(args[0].(string))
		case "checkpoint":
			e = tx.Checkpoint()
		case "purge":
			already := IDSet{}
			if len(args) > 1 {
				for _, id := range args[1].([]any) {
					already[id.(string)] = struct{}{}
				}
			}
			e = p.PurgeHistoryDependents(args[0].(string), seen, already)
		case "invalidate":
			legacy := false
			if len(args) > 1 {
				legacy = args[1].(bool)
			}
			if legacy {
				e = p.InvalidateLegacyDependents(args[0].(string))
			} else {
				e = p.InvalidateDependents(args[0].(string), nil)
			}
		case "refresh":
			var ids []string
			ids, e = p.RefreshSupport()
			if e == nil {
				values := []any{}
				for _, id := range ids {
					values = append(values, id)
				}
				*results = append(*results, values)
			}
		case "depends":
			var yes bool
			yes, e = p.DependsOn(args[0].(string), args[1].(string), nil)
			if e == nil {
				*results = append(*results, yes)
			}
		case "nested":
			e = tx.Transaction(context.Background(), func(child *assertions.RevisionTxn) error { return f.run(t, child, args[0].([]any), results) })
			if e != nil && args[1].(bool) {
				f.events = append(f.events, []any{"caught", e.Error()})
				e = nil
			}
		default:
			return fmt.Errorf("unknown_action:%s", method)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func rowsSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, e := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND (name LIKE 'memory_%' OR name='memories' OR name='vector_spaces') ORDER BY name")
	must(t, e)
	names := []string{}
	for rows.Next() {
		var name string
		must(t, rows.Scan(&name))
		names = append(names, name)
	}
	must(t, errors.Join(rows.Err(), rows.Close()))
	all := new(Object)
	for _, name := range names {
		rows, e := db.Query("SELECT * FROM " + name + " ORDER BY 1,2")
		must(t, e)
		columns, e := rows.Columns()
		must(t, e)
		values := []any{}
		for rows.Next() {
			cells := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range cells {
				dest[i] = &cells[i]
			}
			must(t, rows.Scan(dest...))
			o := new(Object)
			for i, key := range columns {
				v := cells[i]
				if n, ok := v.(int64); ok {
					v = float64(n)
				}
				if bytes, ok := v.([]byte); ok {
					v = string(bytes)
				}
				must(t, o.Set(key, v))
			}
			values = append(values, o)
		}
		must(t, errors.Join(rows.Err(), rows.Close()))
		must(t, all.Set(name, values))
	}
	return stringify(t, all)
}
func (f *fixture) intents(t *testing.T) string {
	t.Helper()
	db, e := sql.Open("sqlite3", filepath.Join(f.authorityPath, "authority.sqlite"))
	must(t, e)
	defer db.Close()
	rows, e := db.Query("SELECT payload FROM revocations ORDER BY sequence")
	must(t, e)
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var raw string
		must(t, rows.Scan(&raw))
		v := parse(t, raw).(*Object)
		v.Delete("operationId")
		out = append(out, v)
	}
	must(t, rows.Err())
	return stringify(t, out)
}
func TestActualSourceProofInvalidation(t *testing.T) {
	var oracle struct {
		Traces []struct {
			Name  string
			Steps []struct {
				Op, Result, Events, RowsHash, Intents string
				Error                                 *string
				GateSequence, MinimumReader           int64
			}
		}
	}
	raw, e := os.ReadFile("testdata/proof-invalidation.json")
	must(t, e)
	must(t, json.Unmarshal(raw, &oracle))
	for _, trace := range oracle.Traces {
		t.Run(trace.Name, func(t *testing.T) {
			f := setup(t)
			for i, step := range trace.Steps {
				f.events = []any{}
				f.fault = ""
				results := []any{}
				op := parse(t, step.Op).([]any)
				e := f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error { return f.run(t, tx, op[1].([]any), &results) })
				if step.Error == nil && e != nil {
					t.Fatalf("step %d: %v", i, e)
				}
				if step.Error != nil && (e == nil || e.Error() != *step.Error) {
					t.Fatalf("step %d error: %v want %s", i, e, *step.Error)
				}
				if got := stringify(t, results); got != step.Result {
					t.Fatalf("step %d result %s want %s", i, got, step.Result)
				}
				if got := stringify(t, f.events); got != step.Events {
					t.Fatalf("step %d events %s want %s", i, got, step.Events)
				}
				rows := rowsSnapshot(t, f.main)
				sum := sha256.Sum256([]byte(rows))
				if got := hex.EncodeToString(sum[:]); got != step.RowsHash {
					t.Fatalf("step %d rows hash %s want %s\n%s", i, got, step.RowsHash, rows)
				}
				if got := f.intents(t); got != step.Intents {
					t.Fatalf("step %d intents %s want %s", i, got, step.Intents)
				}
				gate, e := recovery.ReadGate(context.Background(), f.main)
				must(t, e)
				if gate.Head.Sequence != step.GateSequence || gate.MinimumReader != step.MinimumReader {
					t.Fatalf("step %d gate %+v", i, gate)
				}
			}
		})
	}
}

func TestHistoryFailureLeavesDurableIntentAndBlocksPublication(t *testing.T) {
	f := setup(t)
	f.seed(t, "root")
	before := rowsSnapshot(t, f.main)
	_, e := f.main.Exec(`CREATE TRIGGER reject_purge BEFORE UPDATE ON memory_assertion_state BEGIN SELECT RAISE(ABORT,'synthetic_purge_failure'); END`)
	must(t, e)
	e = f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		return p.PurgeHistoryDependents("root", nil, nil)
	})
	if e == nil || !strings.Contains(e.Error(), "synthetic_purge_failure") {
		t.Fatal(e)
	}
	if rowsSnapshot(t, f.main) != before {
		t.Fatal("main writes escaped rollback")
	}
	if f.intents(t) != `[{"kind":"assertion","id":"root","throughVersion":1,"terminal":false}]` {
		t.Fatal(f.intents(t))
	}
	e = f.store.Read(context.Background(), func(*assertions.Reader) error { t.Fatal("publication passed gap"); return nil })
	if e == nil || !strings.Contains(e.Error(), "recovery_authority_head_changed") {
		t.Fatal(e)
	}
}
func TestVectorFailureRollsBackRowsButNotPrivacyIntents(t *testing.T) {
	f := setup(t)
	f.seed(t, "root")
	_, e := f.main.Exec(`INSERT INTO vector_spaces VALUES('space','{}');INSERT INTO memory_vectors VALUES('root','space','embedding');CREATE TRIGGER reject_vector BEFORE DELETE ON memory_vectors BEGIN SELECT RAISE(ABORT,'synthetic_vector_failure');END`)
	must(t, e)
	before := rowsSnapshot(t, f.main)
	e = f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		if e := tx.Repository.SaveSupports("root", []any{}); e != nil {
			return e
		}
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		_, e = p.RefreshSupport()
		return e
	})
	if e == nil || !strings.Contains(e.Error(), "synthetic_vector_failure") {
		t.Fatal(e)
	}
	if rowsSnapshot(t, f.main) != before {
		t.Fatal("vector/main rollback failed")
	}
	if f.intents(t) == "[]" {
		t.Fatal("intent lost")
	}
}
func TestAnalysisFailureEmitsNoPrivacyIntent(t *testing.T) {
	f := setup(t)
	f.seed(t, "malformed", "unsupported")
	before := rowsSnapshot(t, f.main)
	e := f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		if e := tx.Repository.SaveSupports("unsupported", []any{}); e != nil {
			return e
		}
		if e := tx.Store.WithWriteSession(func(s *recovery.WriteSession) error {
			_, e := s.Exec("UPDATE memory_supports SET value='not-json' WHERE memory_id='malformed'")
			return e
		}); e != nil {
			return e
		}
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		_, e = p.RefreshSupport()
		return e
	})
	if e == nil {
		t.Fatal("corrupt analysis accepted")
	}
	if len(f.events) != 0 || f.intents(t) != "[]" {
		t.Fatalf("partial application %v %s", f.events, f.intents(t))
	}
	if rowsSnapshot(t, f.main) != before {
		t.Fatal("analysis failure changed rows")
	}
}
func TestScopedLifecycleAndOwnerBridge(t *testing.T) {
	f := setup(t)
	other := setup(t)
	f.seed(t, "root")
	var escaped *ProofInvalidation
	var escapedStore *assertions.Store
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		escaped, escapedStore = p, tx.Store
		return tx.Transaction(context.Background(), func(child *assertions.RevisionTxn) error {
			if _, e := p.RefreshSupport(); e == nil || e.Error() != "recovery_session_scope_inactive" {
				t.Fatalf("ancestor accepted: %v", e)
			}
			called := false
			if e := tx.Store.WithWriteSession(func(*recovery.WriteSession) error { called = true; return nil }); e == nil || called {
				t.Fatal("ancestor owner callback ran")
			}
			return child.Store.WithWriteSession(func(s *recovery.WriteSession) error {
				if e := other.store.WithSession(s).Read(context.Background(), func(*assertions.Reader) error { t.Fatal("foreign binding read"); return nil }); e == nil || e.Error() != "recovery_session_binding_mismatch" {
					t.Fatal(e)
				}
				_, e := s.Exec("DELETE FROM memory_vectors WHERE memory_id=?", "root")
				return e
			})
		})
	}))
	if _, e := escaped.RefreshSupport(); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	called := false
	if e := escapedStore.WithWriteSession(func(*recovery.WriteSession) error { called = true; return nil }); !errors.Is(e, sql.ErrTxDone) || called {
		t.Fatal(e)
	}
}
func TestNativeRollbackRevokesLifecycleRoot(t *testing.T) {
	f := setup(t)
	f.seed(t, "root")
	_, e := f.main.Exec(`CREATE TRIGGER fatal_purge BEFORE UPDATE ON memory_assertion_state BEGIN SELECT RAISE(ROLLBACK,'fatal_purge');END`)
	must(t, e)
	e = f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		e = tx.Transaction(context.Background(), func(child *assertions.RevisionTxn) error {
			p, e := New(child, f.owners, f.options)
			if e != nil {
				return e
			}
			return p.PurgeHistoryDependents("root", nil, nil)
		})
		if e == nil || !strings.Contains(e.Error(), "fatal_purge") {
			t.Fatalf("native failure: %v", e)
		}
		if _, e = p.RefreshSupport(); e == nil {
			t.Fatal("native rollback left root usable")
		}
		return nil
	})
	if e == nil {
		t.Fatal("revoked root committed")
	}
	if f.intents(t) == "[]" {
		t.Fatal("fatal rollback lost durable intent")
	}
}
func TestRequiredOwnersCannotBeOmitted(t *testing.T) {
	f := setup(t)
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		_, e := New(tx, Owners{}, f.options)
		if e == nil {
			t.Fatal("missing owners accepted")
		}
		return nil
	}))
}
func TestDependencyGraphIncludesInvalidatedInactiveLinks(t *testing.T) {
	f := setup(t)
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		for _, id := range []string{"root", "inactive", "leaf"} {
			r := record(t, id)
			if id == "inactive" {
				must(t, r.Set("status", "invalidated"))
			}
			if e := tx.Repository.Write(r); e != nil {
				return e
			}
			p := proof(t, id)
			if id == "inactive" {
				p = proof(t, id, "root")
				must(t, p.Set("invalidatedBy", "other"))
			}
			if id == "leaf" {
				p = proof(t, id, "inactive")
			}
			if e := tx.Repository.SaveSupports(id, []any{p}); e != nil {
				return e
			}
		}
		return nil
	}))
	f.events = []any{}
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		return p.PurgeHistoryDependents("root", nil, nil)
	}))
	order := []string{}
	for _, v := range f.events {
		event := v.([]any)
		if event[0] == "beforePurge" {
			order = append(order, event[1].(string))
		}
	}
	if !reflect.DeepEqual(order, []string{"root", "inactive", "leaf"}) {
		t.Fatal(order)
	}
}

func TestHistoricalOnlySafeRebuildPersistsAcrossReopen(t *testing.T) {
	f := setup(t)
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		if e := tx.Repository.Write(record(t, "parent")); e != nil {
			return e
		}
		if e := tx.Repository.SaveSupports("parent", []any{proof(t, "parent")}); e != nil {
			return e
		}
		child := record(t, "child")
		must(t, child.Set("content", "HISTORICAL_ONLY_PRIVATE_CANARY"))
		if e := tx.Repository.Write(child); e != nil {
			return e
		}
		return tx.Repository.SaveSupports("child", []any{proof(t, "child", "parent")})
	}))
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		child := record(t, "child")
		must(t, child.Set("content", "Reviewed safe body"))
		must(t, child.Set("origin", "manual"))
		must(t, child.Set("lineageStatus", "unverified"))
		must(t, child.Set("sources", []any{}))
		if e := tx.Repository.Write(child); e != nil {
			return e
		}
		return tx.Repository.SaveSupports("child", []any{})
	}))
	if !strings.Contains(rowsSnapshot(t, f.main), "HISTORICAL_ONLY_PRIVATE_CANARY") {
		t.Fatal("fixture lacks historical body")
	}
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		return p.PurgeHistoryDependents("parent", nil, nil)
	}))
	if strings.Contains(rowsSnapshot(t, f.main), "HISTORICAL_ONLY_PRIVATE_CANARY") {
		t.Fatal("historical-only body retained")
	}
	before := rowsSnapshot(t, f.main)
	must(t, f.ledger.Close())
	must(t, f.main.Close())
	db, e := sql.Open("sqlite3", filepath.Join(filepath.Dir(f.authorityPath), "live", "main.sqlite")+"?_foreign_keys=on")
	must(t, e)
	defer db.Close()
	ledger, e := recovery.OpenWriter(context.Background(), f.authorityPath)
	must(t, e)
	defer ledger.Close()
	store, e := assertions.New(db, ledger, assertions.Options{})
	must(t, e)
	must(t, store.Read(context.Background(), func(r *assertions.Reader) error {
		state, e := r.State("child")
		if e != nil {
			return e
		}
		if state.DeletionEpoch != 1 || state.Version != 3 {
			t.Fatalf("safe rebuild state %+v", state)
		}
		latest, e := r.LatestRevision("child", "scene:scene")
		if e != nil {
			return e
		}
		if !strings.Contains(stringify(t, latest), "Reviewed safe body") {
			t.Fatal("safe snapshot not rebuilt")
		}
		return nil
	}))
	if rowsSnapshot(t, db) != before {
		t.Fatal("reopen replayed or changed history")
	}
}
func TestDependsOnDeepCycleWithoutTraversalCap(t *testing.T) {
	f := setup(t)
	const size = 15000
	must(t, f.store.Transaction(context.Background(), func(s *assertions.Store) error {
		return s.WithWriteSession(func(session *recovery.WriteSession) error {
			for i := 0; i < size; i++ {
				id := fmt.Sprintf("n-%d", i)
				parent := fmt.Sprintf("n-%d", (i+1)%size)
				r := record(t, id)
				raw := stringify(t, r)
				if _, e := session.Exec("INSERT INTO memories VALUES(?,?,?,?,?,?)", id, "scene:scene", "person", id, id, raw); e != nil {
					return e
				}
				if _, e := session.Exec("INSERT INTO memory_supports VALUES(?,?)", id, stringify(t, []any{proof(t, id, parent)})); e != nil {
					return e
				}
			}
			return nil
		})
	}))
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		seen := IDSet{}
		yes, e := p.DependsOn("n-0", "missing", seen)
		if e != nil {
			return e
		}
		if yes || len(seen) != size {
			t.Fatalf("deep cycle truncated: %v %d", yes, len(seen))
		}
		yes, e = p.DependsOn("n-0", "n-14999", nil)
		if e != nil {
			return e
		}
		if !yes {
			t.Fatal("deep descendant absent")
		}
		return nil
	}))
}

func TestInvalidationPredicateOrderAndFailure(t *testing.T) {
	f := setup(t)
	f.seed(t, "parent", "child")
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		return tx.Repository.SaveSupports("child", []any{proof(t, "first", "parent"), proof(t, "second", "parent"), proof(t, "third", "parent")})
	}))
	before := rowsSnapshot(t, f.main)
	calls := []string{}
	e := f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		return p.InvalidateDependents("parent", func(proof *Object) (bool, error) {
			v, _ := proof.Get("sources")
			id := text(v.([]any)[0].(*Object), "messageId")
			calls = append(calls, id)
			if len(calls) == 5 {
				return false, errors.New("predicate_failure")
			}
			return id != "first", nil
		})
	})
	if e == nil || e.Error() != "predicate_failure" {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(calls, []string{"first", "second", "first", "second", "third"}) {
		t.Fatal(calls)
	}
	if rowsSnapshot(t, f.main) != before || f.intents(t) != "[]" {
		t.Fatal("predicate failure mutated proofs/history")
	}
}

func TestMalformedParentTailDoesNotPreemptShortCircuit(t *testing.T) {
	f := setup(t)
	f.seed(t, "wanted", "child")
	must(t, f.coordinator.Transaction(context.Background(), func(tx *assertions.RevisionTxn) error {
		proof := proof(t, "child", "wanted")
		must(t, proof.Set("parents", []any{"wanted", nil}))
		if e := tx.Repository.SaveSupports("child", []any{proof}); e != nil {
			return e
		}
		p, e := New(tx, f.owners, f.options)
		if e != nil {
			return e
		}
		yes, e := p.DependsOn("child", "wanted", nil)
		if e != nil {
			return e
		}
		if !yes {
			t.Fatal("early matching parent lost")
		}
		if e = p.InvalidateDependents("wanted", nil); e != nil {
			return e
		}
		// The unrelated non-string reverse-graph key is unreachable from an
		// assertion string ID and must not prevent purging the real child edge.
		return p.PurgeHistoryDependents("wanted", nil, nil)
	}))
}
