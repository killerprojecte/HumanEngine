package identityprivacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
)

type fixture struct {
	main      *sql.DB
	authority *recovery.Authority
	path      string
}

func newFixture(t *testing.T) fixture {
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
	data := filepath.Join(root, "data")
	if e := os.Mkdir(data, 0700); e != nil {
		t.Fatal(e)
	}
	main, e := sql.Open("sqlite3", filepath.Join(data, "main.sqlite"))
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
	authority, e := recovery.OpenWriter(context.Background(), path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { authority.Close(); main.Close() })
	return fixture{main, authority, path}
}
func seed(t *testing.T, f fixture, raw string) {
	t.Helper()
	if _, e := f.main.Exec("CREATE TABLE memory_identity_registry(id INTEGER PRIMARY KEY CHECK(id=1),value TEXT NOT NULL);INSERT INTO memory_identity_registry VALUES(1,?)", raw); e != nil {
		t.Fatal(e)
	}
}
func TestActualSourceRecallIdentityOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-recall.json")
	if e != nil {
		t.Fatal(e)
	}
	var oracle struct {
		Cases []struct {
			Name                  string
			Input, After          json.RawMessage
			Scene, Message, Error string
			Events                []json.RawMessage
		}
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	for _, c := range oracle.Cases {
		t.Run(c.Name, func(t *testing.T) {
			f := newFixture(t)
			if string(c.Input) != "null" {
				seed(t, f, string(c.Input))
			}
			e := f.authority.WithWriteSession(context.Background(), f.main, func(s *recovery.WriteSession) error {
				if _, e := s.AppendIntent(recovery.Source{SceneID: c.Scene, MessageID: c.Message}); e != nil {
					return e
				}
				return RecallSource(s, c.Scene, c.Message)
			})
			if (e == nil) != (c.Error == "") || (e != nil && !strings.Contains(e.Error(), c.Error)) {
				t.Fatal(e, c.Error)
			}
			var count int
			if e = f.main.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='memory_identity_registry'").Scan(&count); e != nil {
				t.Fatal(e)
			}
			if string(c.After) == "null" {
				if count != 0 {
					t.Fatal("read created registry")
				}
			} else {
				var actual string
				if e = f.main.QueryRow("SELECT value FROM memory_identity_registry WHERE id=1").Scan(&actual); e != nil {
					t.Fatal(e)
				}
				// Compare actual source serialization, including schema property ordering.
				wanted, e := jsjson.Parse(string(c.After))
				if e != nil {
					t.Fatal(e)
				}
				expected, _ := jsjson.Stringify(wanted)
				got, e := jsjson.Parse(actual)
				if e != nil {
					t.Fatal(e)
				}
				encoded, _ := jsjson.Stringify(got)
				if encoded != expected {
					t.Fatalf("registry mismatch\ngot %s\nwant %s", encoded, expected)
				}
			}
			journal, e := sql.Open("sqlite3", filepath.Join(f.path, "authority.sqlite"))
			if e != nil {
				t.Fatal(e)
			}
			defer journal.Close()
			rows, e := journal.Query("SELECT payload FROM revocations WHERE sequence>1 ORDER BY sequence")
			if e != nil {
				t.Fatal(e)
			}
			defer rows.Close()
			i := 0
			for rows.Next() {
				var payload string
				if e = rows.Scan(&payload); e != nil {
					t.Fatal(e)
				}
				if i >= len(c.Events) {
					t.Fatal("unexpected identity receipt")
				}
				actual, e := jsjson.Parse(payload)
				if e != nil {
					t.Fatal(e)
				}
				o := actual.(*jsjson.Object)
				expected, e := jsjson.Parse(string(c.Events[i]))
				if e != nil {
					t.Fatal(e)
				}
				m := expected.(*jsjson.Object)
				if get(o, "kind") != "identity_mutation" || get(o, "operation") != get(m, "kind") {
					t.Fatal(payload)
				}
				for _, key := range []string{"operationId", "expectedRevision", "nextRevision", "beforeHash", "qualificationHash", "targets"} {
					left, _ := jsjson.Stringify(get(o, key))
					right, _ := jsjson.Stringify(get(m, key))
					if left != right {
						t.Fatalf("%s mismatch: %s != %s", key, left, right)
					}
				}
				i++
			}
			if e = rows.Err(); e != nil || i != len(c.Events) {
				t.Fatal(e, i, len(c.Events))
			}
		})
	}
}

const oneAlias = `{"format":1,"revision":0,"edges":[],"aliases":[{"id":"a","endpoint":{"adapterId":"adapter","accountId":"account","personId":"person"},"label":"Synthetic","scopeSceneId":"s","source":{"kind":"observation","sceneId":"s","messageId":"m","senderId":"person"},"validFrom":null,"validUntil":null}],"operations":[]}`

func TestIdentityRecallFailureLeavesDurableReceiptAndExpires(t *testing.T) {
	for _, kind := range []string{"ABORT", "ROLLBACK"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			seed(t, f, oneAlias)
			if _, e := f.main.Exec("CREATE TRIGGER fail_identity BEFORE UPDATE ON memory_identity_registry BEGIN SELECT RAISE(" + kind + ",'synthetic_identity'); END"); e != nil {
				t.Fatal(e)
			}
			var held *recovery.WriteSession
			e := f.authority.WithWriteSession(context.Background(), f.main, func(s *recovery.WriteSession) error {
				held = s
				if _, e := s.AppendIntent(recovery.Source{SceneID: "s", MessageID: "m"}); e != nil {
					return e
				}
				return RecallSource(s, "s", "m")
			})
			if e == nil {
				t.Fatal("missing fault")
			}
			head, e := f.authority.Head(context.Background())
			if e != nil || head.Sequence != 2 {
				t.Fatal("missing durable identity receipt", head, e)
			}
			gate, e := recovery.ReadGate(context.Background(), f.main)
			if e != nil || gate.Head.Sequence != 0 {
				t.Fatal("ACK treated as business completion", gate, e)
			}
			var actual string
			if e = f.main.QueryRow("SELECT value FROM memory_identity_registry WHERE id=1").Scan(&actual); e != nil || actual != oneAlias {
				t.Fatal(actual, e)
			}
			if e = RecallSource(held, "s", "m"); !errors.Is(e, sql.ErrTxDone) {
				t.Fatal("expired recall", e)
			}
		})
	}
}
