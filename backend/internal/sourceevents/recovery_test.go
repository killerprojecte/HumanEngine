package sourceevents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type recoveryCase struct {
	Name, Stage, Raise                                   string
	CatchInside, ContinueWrite, OuterFailure, Enrollment bool
	Caught, OuterError                                   *struct{ Message string }
	Persisted                                            struct {
		Gate          recovery.Gate
		AuthorityHead recovery.Head
		Heads         []any
		Revisions     []any
		EditKeys      []any
		Markers       []struct{ Value string }
		Readiness     *string
	}
}

func TestActualDualDatabaseFaults(t *testing.T) {
	bytes, e := os.ReadFile("testdata/source-event-recovery.json")
	if e != nil {
		t.Fatal(e)
	}
	var oracle struct{ Cases []recoveryCase }
	if e = json.Unmarshal(bytes, &oracle); e != nil {
		t.Fatal(e)
	}
	for _, c := range oracle.Cases {
		t.Run(c.Name, func(t *testing.T) {
			ctx := context.Background()
			armed := false
			var f fixture
			var other *recovery.Authority
			f = newFixture(t, Options{BeforeWrite: func(s *Store, m Mutation) error {
				if armed && (c.Stage == "before-append" || c.Stage == "after-append") {
					if e := s.session.RequireReader(2); e != nil {
						return e
					}
					if c.Stage == "before-append" {
						return errors.New("synthetic-before-append")
					}
					key, _ := IdentityKey(m.Identity)
					intent := recovery.SourceEditHead{SceneID: m.Identity.SceneID, MessageID: m.Identity.MessageID, EventID: m.Ref.EventID, Revision: m.Ref.Revision, DeletionEpoch: m.Ref.DeletionEpoch, IdentityHash: key, HeadHash: m.HeadHash}
					var raw string
					rows, e := s.session.Query("SELECT value FROM recovery_gate")
					if e != nil {
						return e
					}
					if !rows.Next() {
						return rows.Err()
					}
					e = rows.Scan(&raw)
					rows.Close()
					if e != nil {
						return e
					}
					var g recovery.Gate
					if e = json.Unmarshal([]byte(raw), &g); e != nil {
						return e
					}
					if _, e = f.authority.Append(ctx, intent, g.Head, false); e != nil {
						return e
					}
					return errors.New("synthetic-after-append")
				}
				if e := s.RecordMutation(m); e != nil {
					return e
				}
				if armed && c.Stage == "after-hook" {
					return errors.New("synthetic-after-hook")
				}
				return nil
			}})
			if _, e = f.main.Exec("CREATE TABLE marker(value TEXT UNIQUE);INSERT INTO marker VALUES('original')"); e != nil {
				t.Fatal(e)
			}
			if !c.Enrollment {
				if _, e = f.store.Observe(ctx, ingress()); e != nil {
					t.Fatal(e)
				}
			}
			raise := c.Raise
			if raise == "" {
				raise = "ABORT"
			}
			var trigger string
			switch c.Stage {
			case "reader-save":
				trigger = `CREATE TRIGGER fault BEFORE UPDATE ON recovery_gate WHEN json_extract(NEW.value,'$.minimumReader')>json_extract(OLD.value,'$.minimumReader') BEGIN SELECT RAISE(ABORT,'synthetic-reader-save');END`
			case "gate-save":
				trigger = `CREATE TRIGGER fault BEFORE UPDATE ON recovery_gate WHEN json_extract(NEW.value,'$.head.sequence')>json_extract(OLD.value,'$.head.sequence') BEGIN SELECT RAISE(` + raise + `,'synthetic-gate-save');END`
			case "source-sql":
				trigger = `CREATE TRIGGER fault BEFORE UPDATE ON source_event_heads BEGIN SELECT RAISE(` + raise + `,'synthetic-source-sql');END`
			case "ordinary-constraint":
				trigger = `CREATE TRIGGER fault BEFORE UPDATE ON source_event_heads BEGIN INSERT INTO marker VALUES('original');END`
			}
			if trigger != "" {
				if _, e = f.main.Exec(trigger); e != nil {
					t.Fatal(e)
				}
			}
			f.store.SubscribeCommitted(&Listener{Handle: func(_ *Store, m Mutation) error {
				if !armed {
					return nil
				}
				if c.Stage == "subscriber" {
					return errors.New("synthetic-subscriber")
				}
				if c.Stage == "external-advance" {
					var e error
					var gate recovery.Gate
					gate, e = recovery.ReadGate(ctx, f.main)
					if e != nil {
						return e
					}
					other, e = recovery.OpenWriter(ctx, gate.Authority)
					if e != nil {
						return e
					}
					head, e := other.Head(ctx)
					if e != nil {
						return e
					}
					_, e = other.Append(ctx, recovery.Source{SceneID: "external", MessageID: "advance"}, head, false)
					return e
				}
				return nil
			}})
			armed = true
			var caught error
			e = f.authority.WithWriteSession(ctx, f.main, func(tx *recovery.WriteSession) error {
				if _, e := tx.Exec("INSERT INTO marker VALUES('outer-before')"); e != nil {
					return e
				}
				change := edit(1)
				if c.Enrollment {
					change = ingress()
				}
				_, err := f.store.WithSession(tx).Observe(ctx, change)
				if err != nil {
					if !c.CatchInside {
						return err
					}
					caught = err
				}
				if c.ContinueWrite {
					if _, e := tx.Exec("INSERT INTO marker VALUES('outer-after')"); e != nil {
						return e
					}
				}
				if c.OuterFailure {
					return errors.New("synthetic-outer")
				}
				return nil
			})
			if other != nil {
				other.Close()
			}
			if (caught != nil) != (c.Caught != nil) {
				t.Fatal("caught", caught, c.Caught)
			}
			if (e != nil) != (c.OuterError != nil) {
				t.Fatal("outer", e, c.OuterError)
			}
			gate, err := recovery.ReadGate(ctx, f.main)
			if err != nil {
				t.Fatal(err)
			}
			if gate.Head != c.Persisted.Gate.Head || gate.MinimumReader != c.Persisted.Gate.MinimumReader {
				t.Fatal("gate", gate, c.Persisted.Gate)
			}
			head, err := f.authority.Head(ctx)
			if err != nil || head != c.Persisted.AuthorityHead {
				t.Fatal("authority", head, err, c.Persisted.AuthorityHead)
			}
			for table, expected := range map[string]int{"source_event_heads": len(c.Persisted.Heads), "source_event_revisions": len(c.Persisted.Revisions), "source_event_edit_keys": len(c.Persisted.EditKeys)} {
				var count int
				if err = f.main.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != expected {
					t.Fatal(table, count, expected, err)
				}
			}
			var actualTables map[string]any
			if err = json.Unmarshal([]byte(dump(t, f.main)), &actualTables); err != nil {
				t.Fatal(err)
			}
			for table, expected := range map[string][]any{"source_event_heads": c.Persisted.Heads, "source_event_revisions": c.Persisted.Revisions, "source_event_edit_keys": c.Persisted.EditKeys} {
				if !reflect.DeepEqual(actualTables[table], expected) {
					t.Fatalf("%s material differs\nactual %#v\nexpected %#v", table, actualTables[table], expected)
				}
			}
			var markers int
			_ = f.main.QueryRow("SELECT count(*) FROM marker").Scan(&markers)
			wantMarkers := len(c.Persisted.Markers)
			if c.Raise == "ROLLBACK" && c.ContinueWrite {
				wantMarkers--
			} // Intentional anti-autocommit hardening, measured against source.
			if markers != wantMarkers {
				t.Fatal("markers", markers, wantMarkers)
			}
			readErr := f.store.Read(ctx, func(*View) error { return nil })
			if (readErr != nil) != (c.Persisted.Readiness != nil) {
				t.Fatal("readiness", readErr, c.Persisted.Readiness)
			}
		})
	}
}

func TestReadOnlySourceDoesNotInitialize(t *testing.T) {
	f := newFixture(t, Options{})
	var path string
	var seq int
	var name string
	if e := f.main.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); e != nil {
		t.Fatal(e)
	}
	readonly, e := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer readonly.Close()
	s, e := New(readonly, f.authority, Options{})
	if e != nil {
		t.Fatal(e)
	}
	e = s.Read(context.Background(), func(v *View) error {
		m, e := v.Registered("scene", "message")
		if m != nil {
			t.Fatal(m)
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Observe(context.Background(), ingress()); e == nil {
		t.Fatal("readonly write allowed")
	}
	if actual := dump(t, f.main); actual != "{}" {
		t.Fatal(actual)
	}
}
func TestNoDefaultHeadQuotaAndScopedPlans(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()
	if _, e := f.store.Observe(ctx, ingress()); e != nil {
		t.Fatal(e)
	}
	for _, query := range []string{"SELECT identity_key FROM source_event_heads WHERE scene_id=? AND message_id=?", "SELECT value FROM source_event_revisions WHERE event_id=? AND revision=?"} {
		rows, e := f.main.Query("EXPLAIN QUERY PLAN "+query, "scene", "message")
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			_ = rows.Scan(&id, &parent, &unused, &detail)
			if strings.Contains(detail, "SCAN ") {
				t.Fatal(detail)
			}
		}
		rows.Close()
	}
	if f.store.options.MaxHeadsPerScene != nil || f.store.options.MaxHeadsTotal != nil {
		t.Fatal("default quota")
	}
}

func TestRestartAndSQLiteBackupKeepExactMetadata(t *testing.T) {
	f := newFixture(t, Options{})
	ctx := context.Background()
	first, e := f.store.Observe(ctx, ingress())
	if e != nil {
		t.Fatal(e)
	}
	directory := t.TempDir()
	oldPath := filepath.Join(directory, "old.sqlite")
	if _, e = f.main.Exec("VACUUM INTO ?", oldPath); e != nil {
		t.Fatal(e)
	}
	second, e := f.store.Observe(ctx, edit(1))
	if e != nil {
		t.Fatal(e)
	}
	currentPath := filepath.Join(directory, "current.sqlite")
	if _, e = f.main.Exec("VACUUM INTO ?", currentPath); e != nil {
		t.Fatal(e)
	}
	gate, e := recovery.ReadGate(ctx, f.main)
	if e != nil {
		t.Fatal(e)
	}
	reopened, e := recovery.OpenWriter(ctx, gate.Authority)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	for _, test := range []struct {
		path    string
		ref     Ref
		current bool
	}{{oldPath, *first.Ref, false}, {currentPath, *second.Ref, true}} {
		db, e := sql.Open("sqlite3", test.path)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec("PRAGMA journal_mode=WAL"); e != nil {
			t.Fatal(e)
		}
		s, e := New(db, reopened, Options{})
		if e != nil {
			t.Fatal(e)
		}
		e = s.Read(ctx, func(v *View) error {
			result, e := v.Current(test.ref, fixtureIdentity())
			if e == nil && (!result.Eligible || result.Metadata.BodyHash != strings.Repeat("b", 64)) {
				t.Fatal("snapshot material", result)
			}
			return e
		})
		db.Close()
		if (e == nil) != test.current {
			t.Fatal(test.path, e)
		}
	}
	if e = f.store.RevokeAll(ctx, "scene", "message"); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"source_event_heads", "source_event_revisions", "source_event_edit_keys"} {
		var n int
		if e = f.main.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatal("recall material survived", table, n, e)
		}
	}
	var n int
	_ = f.main.QueryRow("SELECT count(*) FROM source_event_tombstones").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	r, e := f.store.Observe(ctx, ingress())
	if e != nil || r.Status != "recalled" {
		t.Fatal(r, e)
	}
}
