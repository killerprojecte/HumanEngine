package sourceevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
)

type fixture struct {
	main      *sql.DB
	authority *recovery.Authority
	store     *Store
}

func newFixture(t *testing.T, options Options) fixture {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "authority")
	if e := os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", filepath.Join(path, "authority.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE identity(id TEXT PRIMARY KEY,format INTEGER NOT NULL);CREATE TABLE revocations(sequence INTEGER PRIMARY KEY,previous_hash TEXT NOT NULL,payload TEXT NOT NULL,hash TEXT NOT NULL);INSERT INTO identity VALUES('12345678-1234-4123-8123-123456789012',1)`)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	if e := os.Mkdir(filepath.Join(root, "live"), 0700); e != nil {
		t.Fatal(e)
	}
	main, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = main.Exec(`PRAGMA journal_mode=WAL;CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL)`)
	if e != nil {
		t.Fatal(e)
	}
	gate := recovery.Gate{Head: recovery.Head{InstanceID: "12345678-1234-4123-8123-123456789012", Hash: strings.Repeat("0", 64)}, Authority: path, MinimumReader: 1, State: "ready"}
	raw, _ := json.Marshal(gate)
	if _, e = main.Exec("INSERT INTO recovery_gate VALUES(1,?)", string(raw)); e != nil {
		t.Fatal(e)
	}
	a, e := recovery.OpenWriter(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	if options.ID == nil {
		serial := 0
		options.ID = func() string { serial++; return fmt.Sprintf("event-%d", serial) }
	}
	if options.Now == nil {
		options.Now = func() string { return "2026-10-28T00:00:00.000Z" }
	}
	s, e := New(main, a, options)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close(); main.Close() })
	return fixture{main, a, s}
}
func fixtureIdentity() Identity {
	return Identity{"scene", "synthetic", "account", "message", "speaker"}
}
func ingress() Change {
	return Change{Kind: "ingress", Identity: fixtureIdentity(), BodyHash: strings.Repeat("a", 64)}
}
func edit(n int64) Change {
	c := ingress()
	c.Kind = "edit"
	c.EditID = fmt.Sprintf("edit-%d", n)
	c.Order = Order{Kind: "platform_sequence", Sequence: n}
	c.BodyHash = strings.Repeat("b", 64)
	return c
}
func dump(t *testing.T, db *sql.DB) string {
	t.Helper()
	tables, e := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'source_event_%' ORDER BY name")
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	for tables.Next() {
		var name string
		if e = tables.Scan(&name); e != nil {
			t.Fatal(e)
		}
		names = append(names, name)
	}
	tables.Close()
	o := new(jsjson.Object)
	for _, name := range names {
		rows, e := db.Query("SELECT * FROM " + name + " ORDER BY 1,2")
		if e != nil {
			t.Fatal(e)
		}
		columns, _ := rows.Columns()
		array := []any{}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if e = rows.Scan(targets...); e != nil {
				t.Fatal(e)
			}
			row := new(jsjson.Object)
			for i, k := range columns {
				v := values[i]
				if n, ok := v.(int64); ok {
					v = float64(n)
				}
				_ = row.Set(k, v)
			}
			array = append(array, row)
		}
		rows.Close()
		_ = o.Set(name, array)
	}
	return stringify(t, o)
}
func invoke(t *testing.T, s *Store, operation []any) (any, error) {
	t.Helper()
	ctx := context.Background()
	switch operation[0] {
	case "observe":
		c, e := ParseChange(operation[1])
		if e != nil {
			return nil, e
		}
		r, e := s.Observe(ctx, c)
		if e != nil {
			return nil, e
		}
		return r.Value(), nil
	case "revokeAll":
		return nil, s.RevokeAll(ctx, operation[1].(string), operation[2].(string))
	}
	var out any
	err := s.Read(ctx, func(v *View) error {
		var e error
		switch operation[0] {
		case "registered":
			var m *Metadata
			m, e = v.Registered(operation[1].(string), operation[2].(string))
			if m != nil {
				out = m.Value()
			}
		case "headMetadata":
			i, _ := ParseIdentity(operation[1])
			var m *Metadata
			m, e = v.HeadMetadata(i)
			if m != nil {
				out = m.Value()
			}
		case "current", "historical":
			r, _ := ParseRef(operation[1])
			i, _ := ParseIdentity(operation[2])
			var result Eligibility
			if operation[0] == "current" {
				result, e = v.Current(r, i)
			} else {
				result, e = v.Historical(r, i)
			}
			if e == nil {
				out = result.Value()
			}
		case "hasEdits":
			i, _ := ParseIdentity(operation[1])
			out, e = v.HasEdits(i)
		case "capture":
			i, _ := ParseIdentity(operation[1])
			r, _ := ParseRef(operation[3])
			var captured *Ref
			captured, e = v.Capture(i, operation[2].(string), r)
			if captured != nil {
				out = captured.Value()
			}
		default:
			return fmt.Errorf("unknown operation %v", operation[0])
		}
		return e
	})
	return out, err
}
func TestActualSourceStoreTransitions(t *testing.T) {
	for _, trace := range readOracle(t).Traces {
		t.Run(trace.Name, func(t *testing.T) {
			var callbacks []any
			options := Options{BeforeWrite: func(s *Store, m Mutation) error {
				callbacks = append(callbacks, []any{"before", m.Value()})
				return s.RecordMutation(m)
			}}
			for k, value := range trace.Options {
				n := value
				switch k {
				case "maxRevisions":
					options.MaxRevisions = &n
				case "maxEditKeys":
					options.MaxEditKeys = &n
				case "maxHeadsPerScene":
					options.MaxHeadsPerScene = &n
				case "maxHeadsTotal":
					options.MaxHeadsTotal = &n
				}
			}
			f := newFixture(t, options)
			f.store.SubscribeCommitted(&Listener{Handle: func(_ *Store, m Mutation) error {
				callbacks = append(callbacks, []any{"committed", m.Value()})
				return nil
			}})
			for n, step := range trace.Steps {
				out, e := invoke(t, f.store, parse(t, step.Op).([]any))
				if (e != nil) != (step.Error != "") {
					t.Fatalf("step %d error=%v want=%s", n, e, step.Error)
				}
				if e == nil && stringify(t, out) != step.Result {
					t.Fatalf("step %d result=%s want=%s", n, stringify(t, out), step.Result)
				}
				if stringify(t, callbacks) != step.Callbacks {
					t.Fatalf("step %d callbacks=%s want=%s", n, stringify(t, callbacks), step.Callbacks)
				}
				callbacks = nil
				if actual := dump(t, f.main); actual != step.Rows {
					t.Fatalf("step %d SQLite rows differ\nactual %s\nexpected %s", n, actual, step.Rows)
				}
			}
		})
	}
}
func TestActualSourceCallbackFailures(t *testing.T) {
	for _, c := range readOracle(t).Faults {
		t.Run(fmt.Sprintf("%s/%t/%t", c.Stage, c.Outer, c.Caught), func(t *testing.T) {
			armed := false
			var calls [][]string
			f := newFixture(t, Options{BeforeWrite: func(_ *Store, m Mutation) error {
				calls = append(calls, []string{"before", m.Kind})
				if armed && c.Stage == "before" {
					return errors.New("synthetic-before")
				}
				return nil
			}})
			f.store.SubscribeCommitted(&Listener{Handle: func(_ *Store, m Mutation) error {
				calls = append(calls, []string{"committed", m.Kind})
				if armed && c.Stage == "committed" {
					return errors.New("synthetic-committed")
				}
				return nil
			}})
			if _, e := f.store.Observe(context.Background(), ingress()); e != nil {
				t.Fatal(e)
			}
			calls = nil
			armed = true
			action := func(s *Store) error {
				_, e := s.Observe(context.Background(), edit(1))
				if e != nil && c.Caught {
					calls = append(calls, []string{"caught", e.Error()})
					return nil
				}
				return e
			}
			var e error
			if c.Outer {
				e = f.authority.WithWriteSession(context.Background(), f.main, func(tx *recovery.WriteSession) error { return action(f.store.WithSession(tx)) })
			} else {
				e = action(f.store)
			}
			if (e != nil) != (c.Error != nil) {
				t.Fatal(e, c.Error)
			}
			if !reflect.DeepEqual(calls, c.Calls) {
				t.Fatal(calls, c.Calls)
			}
			var count int
			_ = f.main.QueryRow("SELECT count(*) FROM source_event_revisions").Scan(&count)
			if count != c.RevisionCount {
				t.Fatal(count, c.RevisionCount)
			}
		})
	}
}

func TestListenerLiveSetAndNestedTransaction(t *testing.T) {
	for _, mode := range []string{"remove-next-add-new", "delete-readd-current", "same-function-twice", "nested-observe", "mutate-listener-copy"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, Options{})
			var calls []string
			changed := false
			var stopA, stopB func()
			record := func(name string, m Mutation) { calls = append(calls, name+":"+m.Identity.MessageID) }
			c := &Listener{Handle: func(_ *Store, m Mutation) error { record("C", m); return nil }}
			a := &Listener{}
			a.Handle = func(s *Store, m Mutation) error {
				record("A", m)
				if !changed {
					changed = true
					switch mode {
					case "remove-next-add-new":
						stopB()
						s.SubscribeCommitted(c)
					case "delete-readd-current":
						stopA()
						s.SubscribeCommitted(a)
					case "nested-observe":
						s.SubscribeCommitted(c)
						nested := ingress()
						nested.Identity.MessageID = "nested"
						if _, e := s.Observe(context.Background(), nested); e != nil {
							return e
						}
					}
				}
				if mode == "mutate-listener-copy" {
					m.Ref.Revision = 999
					m.Metadata.Ref.Revision = 999
					m.Identity.MessageID = "changed"
				}
				return nil
			}
			stopA = f.store.SubscribeCommitted(a)
			stopB = f.store.SubscribeCommitted(&Listener{Handle: func(_ *Store, m Mutation) error {
				record("B", m)
				if m.Ref.Revision != 1 || m.Metadata.Ref.Revision != 1 {
					t.Fatal("mutation clone leaked")
				}
				return nil
			}})
			if mode == "same-function-twice" {
				f.store.SubscribeCommitted(a)
			}
			if _, e := f.store.Observe(context.Background(), ingress()); e != nil {
				t.Fatal(e)
			}
			expected := []string{"A:message", "B:message"}
			switch mode {
			case "remove-next-add-new":
				expected = []string{"A:message", "C:message"}
			case "delete-readd-current":
				expected = append(expected, "A:message")
			case "nested-observe":
				expected = []string{"A:message", "A:nested", "B:nested", "C:nested", "B:message", "C:message"}
			}
			if !reflect.DeepEqual(calls, expected) {
				t.Fatal(calls, expected)
			}
		})
	}
}
func TestReadSchemaScopeAndLifecycle(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()
	var retained *View
	if e := f.store.Read(ctx, func(v *View) error {
		retained = v
		m, e := v.Registered("scene", "message")
		if m != nil {
			t.Fatal(m)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := retained.Registered("scene", "message"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatal(e)
	}
	if e := f.store.EnsureLookupIndex(ctx); e != nil {
		t.Fatal(e)
	}
	if actual := dump(t, f.main); actual != "{}" {
		t.Fatal(actual)
	}
	_, e := f.main.Exec(`CREATE TABLE source_event_heads(identity_key TEXT PRIMARY KEY,event_id TEXT UNIQUE NOT NULL,scene_id TEXT NOT NULL,message_id TEXT NOT NULL,value TEXT NOT NULL)`)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.EnsureLookupIndex(ctx); e != nil {
		t.Fatal(e)
	}
	e = f.store.Read(ctx, func(v *View) error { _, e := v.Registered("scene", "message"); return e })
	if !errors.Is(e, ErrCorrupt) {
		t.Fatal(e)
	}
	if _, e = f.store.Observe(ctx, ingress()); e != nil {
		t.Fatal(e)
	}
	_, e = f.main.Exec("DROP TABLE source_event_edit_keys")
	if e != nil {
		t.Fatal(e)
	}
	e = f.store.Read(ctx, func(v *View) error { _, e := v.Registered("scene", "message"); return e })
	if !errors.Is(e, ErrCorrupt) {
		t.Fatal(e)
	}
}

func TestBoundStoreRejectsForeignSessionEveryEntry(t *testing.T) {
	f := newFixture(t, Options{})
	other := newFixture(t, Options{})
	ctx := context.Background()
	e := other.authority.WithWriteSession(ctx, other.main, func(tx *recovery.WriteSession) error {
		s := f.store.WithSession(tx)
		calls := []func() error{func() error { _, e := s.Observe(ctx, ingress()); return e }, func() error { return s.RevokeAll(ctx, "scene", "message") }, func() error { return s.Read(ctx, func(*View) error { t.Fatal("foreign read invoked"); return nil }) }, func() error { return s.EnsureLookupIndex(ctx) }, func() error { return s.RecordMutation(Mutation{Kind: "new"}) }}
		for _, call := range calls {
			if e := call(); e == nil || !strings.Contains(e.Error(), "binding_mismatch") {
				t.Fatal(e)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	gate, e := recovery.ReadGate(ctx, other.main)
	if e != nil || gate.MinimumReader != 1 {
		t.Fatal(gate, e)
	}
}
func TestLocalReadinessHookRunsInsideReadSnapshot(t *testing.T) {
	calls := 0
	reject := false
	f := newFixture(t, Options{AssertReady: func() error {
		calls++
		if reject {
			return errors.New("local-not-ready")
		}
		return nil
	}})
	ctx := context.Background()
	r, e := f.store.Observe(ctx, ingress())
	if e != nil {
		t.Fatal(e)
	}
	before := calls
	e = f.store.Read(ctx, func(v *View) error {
		eligible, e := v.Current(*r.Ref, fixtureIdentity())
		if !eligible.Eligible {
			t.Fatal(eligible)
		}
		return e
	})
	if e != nil || calls <= before {
		t.Fatal(e, calls, before)
	}
	reject = true
	e = f.store.Read(ctx, func(v *View) error { _, e := v.Registered("scene", "message"); return e })
	if e == nil || e.Error() != "local-not-ready" {
		t.Fatal(e)
	}
}
