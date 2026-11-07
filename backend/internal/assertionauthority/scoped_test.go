package assertionauthority

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	_ "github.com/mattn/go-sqlite3"
)

type scopedFixture struct {
	main     *sql.DB
	recovery *recovery.Authority
	store    *assertions.Store
	events   *sourceevents.Store
}

func scopedSetup(t *testing.T) scopedFixture {
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
	events, e := sourceevents.New(main, ra, sourceevents.Options{ID: func() string { return "source-event" }, Now: func() string { return "2026-10-28T12:00:00.000Z" }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ra.Close(); main.Close() })
	return scopedFixture{main, ra, store, events}
}
func parseObject(t *testing.T, raw string) *Object {
	t.Helper()
	v, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v.(*Object)
}
func testOptions(t *testing.T) Options {
	t.Helper()
	p, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	return Options{DateParser: p, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }, NowMillis: func() int64 { return 1793188800000 }}
}
func requireBool(t *testing.T, got bool, e error, want bool) {
	t.Helper()
	if e != nil || got != want {
		t.Fatalf("got %v, %v; want %v", got, e, want)
	}
}

func TestRealScopedSourceEditRecallAndExpiration(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	source := parseObject(t, `{"sceneId":"s","messageId":"m","senderId":"p"}`)
	identity := sourceevents.Identity{SceneID: "s", AdapterID: "adapter", AccountID: "account", MessageID: "m", SenderID: "p"}
	var escaped *Authority
	var escapedEvents *sourceevents.View
	e := f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
		return f.store.WithSession(session).Read(ctx, func(reader *assertions.Reader) error {
			a, e := New(reader, testOptions(t))
			if e != nil {
				return e
			}
			escaped = a
			escapedEvents, e = reader.AuthoritySourceEvents()
			if e != nil {
				return e
			}
			// A missing source schema is a legacy source, not a cached absence:
			// creation in this same transaction must be visible on the next read.
			got, e := a.DomainSourceCurrent(source)
			requireBool(t, got, e, true)
			first, e := f.events.WithSession(session).Observe(ctx, sourceevents.Change{Kind: "ingress", Identity: identity, BodyHash: strings.Repeat("1", 64)})
			if e != nil {
				return e
			}
			if first.Ref == nil {
				t.Fatal(first)
			}
			got, e = a.DomainSourceCurrent(source)
			requireBool(t, got, e, true)
			bound := source.Clone()
			_ = bound.Set("eventRef", first.Ref.Value())
			got, e = a.DomainSourceCurrent(bound)
			requireBool(t, got, e, true)
			updated, e := f.events.WithSession(session).Observe(ctx, sourceevents.Change{Kind: "edit", Identity: identity, BodyHash: strings.Repeat("2", 64), EditID: "edit-1", Order: sourceevents.Order{Kind: "platform_sequence", Sequence: 1}})
			if e != nil {
				return e
			}
			if updated.Ref == nil {
				t.Fatal(updated)
			}
			got, e = a.DomainSourceCurrent(source)
			requireBool(t, got, e, false)
			got, e = a.DomainSourceCurrent(bound)
			requireBool(t, got, e, false)
			_ = bound.Set("eventRef", updated.Ref.Value())
			got, e = a.DomainSourceCurrent(bound)
			requireBool(t, got, e, true)
			// The scene is also read from this transaction, without a second pool
			// connection. A changed account must immediately reject old bindings.
			if _, e = session.Exec(`UPDATE scenes SET value='{"id":"s","adapterId":"adapter","accountId":"other"}' WHERE id='s'`); e != nil {
				return e
			}
			got, e = a.DomainSourceCurrent(bound)
			requireBool(t, got, e, false)
			if _, e = session.Exec(`UPDATE scenes SET value='{"id":"s","adapterId":"adapter","accountId":"account"}' WHERE id='s'`); e != nil {
				return e
			}
			if _, e = session.Exec("INSERT INTO recall_tombstones VALUES('s','m','2026-10-28T12:00:00.000Z')"); e != nil {
				return e
			}
			got, e = a.DomainSourceCurrent(bound)
			requireBool(t, got, e, false)
			return nil
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = escaped.DomainSourceCurrent(source); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped authority: %v", e)
	}
	if _, e = escaped.Supported(parseObject(t, `{"id":"unverified","status":"active","sources":[]}`)); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("zero-read escaped predicate: %v", e)
	}
	if _, e = escapedEvents.Registered("s", "m"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped source view: %v", e)
	}
}

func TestRealScopedAncestorSuspensionAndChildRollback(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	source := parseObject(t, `{"sceneId":"s","messageId":"m","senderId":"p"}`)
	e := f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
		return f.store.WithSession(session).Read(ctx, func(reader *assertions.Reader) error {
			return reader.WithSupportReadStatements(func(outer *assertions.Reader) error {
				a, e := New(outer, testOptions(t))
				if e != nil {
					return e
				}
				view, e := outer.AuthoritySourceEvents()
				if e != nil {
					return e
				}
				rollback := errors.New("synthetic_child_rollback")
				e = session.WithSavepoint(func(child *recovery.WriteSession) error {
					if _, e := a.DomainSourceCurrent(source); e == nil {
						t.Fatal("ancestor authority used during child")
					}
					if _, e := view.Registered("s", "m"); e == nil {
						t.Fatal("ancestor source view used during child")
					}
					return f.store.WithSession(child).Read(ctx, func(r *assertions.Reader) error {
						ca, e := New(r, testOptions(t))
						if e != nil {
							return e
						}
						if _, e = child.Exec("INSERT INTO recall_tombstones VALUES('s','m','2026-10-28T12:00:00.000Z')"); e != nil {
							return e
						}
						got, e := ca.DomainSourceCurrent(source)
						requireBool(t, got, e, false)
						return rollback
					})
				})
				if !errors.Is(e, rollback) {
					return e
				}
				got, e := a.DomainSourceCurrent(source)
				requireBool(t, got, e, true)
				return nil
			})
		})
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestRealPublicationScopeAndMissingDependencies(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	var escaped *Authority
	var scoped *Authority
	if _, e := New(nil, testOptions(t)); e == nil {
		t.Fatal("missing reader accepted")
	}
	e := f.store.Read(ctx, func(reader *assertions.Reader) error {
		if _, e := New(reader, Options{}); e == nil {
			t.Fatal("missing time providers accepted")
		}
		a, e := New(reader, testOptions(t))
		if e != nil {
			return e
		}
		escaped = a
		e = reader.WithSupportReadStatements(func(r *assertions.Reader) error { var e error; scoped, e = New(r, testOptions(t)); return e })
		if e != nil {
			return e
		}
		if _, e = scoped.Supported(parseObject(t, `{"id":"a","status":"active","sources":[]}`)); !errors.Is(e, sql.ErrTxDone) {
			t.Fatalf("expired nested read: %v", e)
		}
		got, e := a.DomainSourceCurrent(parseObject(t, `{"sceneId":"s","messageId":"legacy","senderId":"p"}`))
		requireBool(t, got, e, true)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = escaped.HistoricalAssertion(Context{Simulation: true}, Reference{}); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("expired simulated exposure: %v", e)
	}
}

func TestRealAssertionReaderKeepsPrivacyAndCurrentGatesSeparate(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	err := f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
		bound := f.store.WithSession(session)
		parent := parseObject(t, `{"id":"parent","scope":{"kind":"scene","sceneId":"s","personId":"p"},"content":"Synthetic parent","kind":"claim","sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"status":"active","lineageStatus":"verified","learnedAt":"2026-01-01T00:00:00.000Z","origin":"learned"}`)
		root := clone(parent).(*Object)
		_ = root.Set("id", "root")
		_ = root.Set("content", "Synthetic child")
		parentProof := parseObject(t, `{"sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"parents":[],"parentReferences":[]}`)
		rootProof := parseObject(t, `{"sources":[{"sceneId":"s","messageId":"m","senderId":"p"}],"parents":["parent"],"parentReferences":[{"id":"parent","version":1,"deletionEpoch":0}]}`)
		for _, pair := range []struct{ record, proof *Object }{{parent, parentProof}, {root, rootProof}} {
			id := str(get(pair.record, "id"))
			raw, e := jsjson.Stringify(pair.record)
			if e != nil {
				return e
			}
			proofs := []any{pair.proof}
			proofRaw, e := jsjson.Stringify(proofs)
			if e != nil {
				return e
			}
			if _, e = session.Exec("INSERT INTO memories VALUES(?,?,?,?,?,?)", id, "scene:s", "p", get(pair.record, "content"), id, raw); e != nil {
				return e
			}
			if _, e = session.Exec("INSERT INTO memory_supports VALUES(?,?)", id, proofRaw); e != nil {
				return e
			}
			input := parseObject(t, `{"expectedVersion":0,"expectedDeletionEpoch":0,"idempotencyKey":"first","recordedAt":"2026-01-01T00:00:00.000Z","validFrom":null,"validTo":null,"eventKind":"learned_assertion"}`)
			_ = input.Set("record", pair.record)
			_ = input.Set("supports", proofs)
			if _, e = bound.Append(input); e != nil {
				return e
			}
		}
		return bound.Read(ctx, func(reader *assertions.Reader) error {
			return reader.WithSupportReadStatements(func(reader *assertions.Reader) error {
				a, e := New(reader, testOptions(t))
				if e != nil {
					return e
				}
				current, e := reader.Record("root")
				if e != nil {
					return e
				}
				got, e := a.Supported(current)
				requireBool(t, got, e, true)
				got, e = a.CurrentApplicable(current)
				requireBool(t, got, e, true)
				// A later retained parent revision must not erase the old proof. The
				// current gate rejects it while historical exposure remains available.
				input := parseObject(t, `{"expectedVersion":1,"expectedDeletionEpoch":0,"idempotencyKey":"second","recordedAt":"2026-01-02T00:00:00.000Z","validFrom":null,"validTo":null,"eventKind":"learned_assertion"}`)
				_ = input.Set("record", parent)
				_ = input.Set("supports", []any{parentProof})
				if _, e = bound.Append(input); e != nil {
					return e
				}
				got, e = a.Supported(current)
				requireBool(t, got, e, true)
				got, e = a.CurrentApplicable(current)
				requireBool(t, got, e, false)
				history, e := a.HistoricalAssertion(Context{SceneID: "s"}, Reference{ID: "root", Version: 1})
				if e != nil {
					return e
				}
				if history == nil || len(history.Proofs) != 1 {
					t.Fatal("old revision not available")
				}
				relation, e := a.RelationAuthority(Context{SceneID: "s"}, Reference{ID: "root", Version: 1})
				if e != nil {
					return e
				}
				if relation != nil {
					t.Fatal("stale parent exposed as current")
				}
				if _, e = session.Exec("INSERT INTO recall_tombstones VALUES('s','m','2026-10-28T12:00:00.000Z')"); e != nil {
					return e
				}
				got, e = a.Supported(current)
				requireBool(t, got, e, false)
				history, e = a.HistoricalAssertion(Context{SceneID: "s"}, Reference{ID: "root", Version: 1})
				if e != nil {
					return e
				}
				if history != nil {
					t.Fatal("recalled history exposed")
				}
				return nil
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}
