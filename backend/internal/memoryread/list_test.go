package memoryread

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
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
func parseObject(t *testing.T, raw string) *assertions.Object {
	t.Helper()
	v, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v.(*assertions.Object)
}
func testOptions(t *testing.T) assertionauthority.Options {
	t.Helper()
	p, e := jsdate.New("UTC")
	if e != nil {
		t.Fatal(e)
	}
	return assertionauthority.Options{DateParser: p, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }, NowMillis: func() int64 { return 1793188800000 }}
}

func TestListRealAuthorityScopesAndFreshness(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	var escaped *Service
	put := func(id, scope, extra string) {
		t.Helper()
		raw := `{"id":"` + id + `","scope":` + scope + `,"content":"synthetic","kind":"experience","status":"active","learnedAt":"2026-01-01","sources":[]` + extra + `}`
		scopeKey := "scene:s"
		if strings.Contains(scope, "sharedPublicKnowledge") {
			scopeKey = "shared:public"
		}
		if _, e := f.main.Exec("INSERT INTO memories VALUES(?,?,?,?,?,?)", id, scopeKey, "p", id, id, raw); e != nil {
			t.Fatal(e)
		}
	}
	put("old", `{"kind":"scene","sceneId":"s","personId":"p"}`, "")
	put("shared", `{"kind":"sharedPublicKnowledge"}`, `,"observedAt":"private","supersedesId":"private"`)
	put("new", `{"kind":"scene","sceneId":"s","personId":"p"}`, "")
	put("internal", `{"kind":"scene","sceneId":"s"}`, `,"internalUse":false`)
	put("expired", `{"kind":"scene","sceneId":"s"}`, `,"validUntil":"2000-01-01"`)
	put("invalid-time", `{"kind":"scene","sceneId":"s"}`, `,"validFrom":"not-a-date"`)
	put("quarantined", `{"kind":"scene","sceneId":"s"}`, `,"lineageStatus":"quarantined"`)
	put("unsupported", `{"kind":"scene","sceneId":"s"}`, `,"lineageStatus":"verified"`)
	check := func(s *Service, input Input, want string) {
		t.Helper()
		rows, e := s.List(input)
		if e != nil {
			t.Fatal(e)
		}
		ids := []string{}
		for _, r := range rows {
			id, _ := r.Get("id")
			ids = append(ids, id.(string))
			if id == "shared" {
				if _, ok := r.Get("observedAt"); ok {
					t.Fatal("shared leak")
				}
			}
		}
		if strings.Join(ids, ",") != want {
			t.Fatalf("got %v want %s", ids, want)
		}
	}
	e := f.store.Read(ctx, func(r *assertions.Reader) error {
		s, e := New(r, testOptions(t))
		if e != nil {
			return e
		}
		escaped = s
		check(s, Input{SceneID: "s"}, "new,shared,old")
		check(s, Input{SceneID: "s", IDs: []string{"old", "new", "old"}}, "new,old")
		check(s, Input{SceneID: "s", IDs: []string{}}, "")
		check(s, Input{SceneID: "s", PersonID: "p"}, "new,old")
		check(s, Input{Simulation: true}, "shared")
		check(s, Input{Simulation: true, PersonID: "p"}, "")
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := escaped.List(Input{Simulation: true, PersonID: "p"}); e == nil {
		t.Fatal("expired scope accepted")
	}
	e = f.store.Transaction(ctx, func(s *assertions.Store) error {
		return s.Read(ctx, func(r *assertions.Reader) error {
			service, e := New(r, testOptions(t))
			if e != nil {
				return e
			}
			check(service, Input{SceneID: "s", IDs: []string{"new"}}, "new")
			if e := s.WithWriteSession(func(session *recovery.WriteSession) error {
				_, e := session.Exec("UPDATE memories SET value=json_set(value,'$.status','invalidated') WHERE id='new'")
				return e
			}); e != nil {
				return e
			}
			check(service, Input{SceneID: "s", IDs: []string{"new"}}, "")
			return nil
		})
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestListFailureNeverPublishesPrefix(t *testing.T) {
	f := scopedSetup(t)
	ctx := context.Background()
	// Last-in first-out: a valid record precedes a malformed source array. The
	// source filter fails before sanitize, and no partial records escape.
	for _, entry := range []struct{ id, sources string }{{"broken", "null"}, {"good", "[]"}} {
		raw := `{"id":"` + entry.id + `","scope":{"kind":"scene","sceneId":"s"},"status":"active","sources":` + entry.sources + `}`
		if _, e := f.main.Exec("INSERT INTO memories VALUES(?,?,?,?,?,?)", entry.id, "scene:s", nil, entry.id, entry.id, raw); e != nil {
			t.Fatal(e)
		}
	}
	e := f.store.Read(ctx, func(r *assertions.Reader) error {
		s, e := New(r, testOptions(t))
		if e != nil {
			return e
		}
		out, e := s.List(Input{SceneID: "s"})
		if e == nil || out != nil {
			t.Fatalf("partial success %v %v", out, e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestListRejectsSuspendedForeignAndReadinessCapabilities(t *testing.T) {
	f := scopedSetup(t)
	other := scopedSetup(t)
	ctx := context.Background()
	e := f.recovery.WithWriteSession(ctx, f.main, func(session *recovery.WriteSession) error {
		if e := other.store.WithSession(session).Read(ctx, func(*assertions.Reader) error { t.Fatal("foreign reader callback"); return nil }); e == nil {
			t.Fatal("foreign binding accepted")
		}
		return f.store.WithSession(session).Read(ctx, func(r *assertions.Reader) error {
			s, e := New(r, testOptions(t))
			if e != nil {
				return e
			}
			return session.WithSavepoint(func(child *recovery.WriteSession) error {
				if _, e := s.List(Input{Simulation: true, PersonID: "p"}); e == nil {
					t.Fatal("suspended ancestor accepted")
				}
				return f.store.WithSession(child).Read(ctx, func(r *assertions.Reader) error {
					active, e := New(r, testOptions(t))
					if e != nil {
						return e
					}
					_, e = active.List(Input{Simulation: true, PersonID: "p"})
					return e
				})
			})
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	blocked, e := assertions.New(f.main, f.recovery, assertions.Options{AssertReady: func() error { return fmt.Errorf("synthetic-not-ready") }})
	if e != nil {
		t.Fatal(e)
	}
	e = blocked.Read(ctx, func(r *assertions.Reader) error {
		s, e := New(r, testOptions(t))
		if e != nil {
			return e
		}
		out, e := s.List(Input{Simulation: true, PersonID: "p"})
		if e == nil || e.Error() != "synthetic-not-ready" || out != nil {
			t.Fatalf("readiness skipped: %v %v", out, e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
