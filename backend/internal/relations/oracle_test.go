package relations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"
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
	storage    *relationstorage.Store
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
	relations, e := relationstorage.New(main, ra)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ra.Close(); main.Close() })
	if e = relations.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	return &fixture{main: main, ledger: ra, assertions: store, storage: relations}
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
func contextValue(v any) Context {
	o := object(v)
	c := Context{SceneID: str(get(o, "sceneId")), Simulation: get(o, "simulation") == true, Provenance: str(get(o, "provenance"))}
	for _, v := range arr(get(o, "exposedEntities")) {
		c.ExposedEntities = append(c.ExposedEntities, object(v))
	}
	return c
}
func viewValue(v any) *assertionauthority.Result {
	if v == nil {
		return nil
	}
	return &assertionauthority.Result{Record: object(get(v, "record")), Proofs: arr(get(v, "proofs"))}
}
func listValue(v Listing) *Object {
	a := []any{}
	for _, r := range v.Items {
		a = append(a, r)
	}
	return obj("items", a, "coverage", v.Coverage)
}
func admissionValue(v Admission) *Object {
	a := []any{}
	for _, r := range v.Created {
		a = append(a, r)
	}
	b := []any{}
	for _, r := range v.Rejected {
		b = append(b, obj("index", float64(r.Index), "reason", r.Reason))
	}
	return obj("created", a, "rejected", b)
}
func stringArray(a []string) []any {
	out := []any{}
	for _, v := range a {
		out = append(out, v)
	}
	return out
}
func state(t *testing.T, db *sql.DB) *Object {
	t.Helper()
	out := new(Object)
	for _, pair := range [][2]string{{"entities", "memory_relation_entities"}, {"candidates", "memory_relation_candidates"}, {"tombstones", "memory_relation_tombstones"}} {
		rows, e := db.Query("SELECT * FROM " + pair[1])
		must(t, e)
		columns, e := rows.Columns()
		must(t, e)
		values := []any{}
		for rows.Next() {
			scan := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range scan {
				pointers[i] = &scan[i]
			}
			must(t, rows.Scan(pointers...))
			o := new(Object)
			for i, k := range columns {
				v := scan[i]
				switch x := v.(type) {
				case int64:
					v = float64(x)
				case []byte:
					v = string(x)
				}
				_ = o.Set(k, v)
			}
			values = append(values, o)
		}
		must(t, rows.Err())
		must(t, rows.Close())
		_ = out.Set(pair[0], values)
	}
	return out
}
func TestSourceOracle(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-oracle.json")
	must(t, e)
	root := parse(t, string(raw))
	for _, schema := range arr(get(root, "schemas")) {
		out, e := parseProposal(get(schema, "input"))
		if (e == nil) != (get(schema, "success") == true) {
			t.Fatalf("schema mismatch %s: %v", jsonText(t, get(schema, "input")), e)
		}
		if e == nil && jsonText(t, out) != jsonText(t, get(schema, "data")) {
			t.Fatal("schema field order mismatch")
		}
	}
	for _, input := range arr(get(root, "scenarios")) {
		t.Run(str(get(input, "name")), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			currentContext := clone(get(input, "context")).(*Object)
			flags := clone(get(input, "flags")).(*Object)
			views := clone(get(input, "views")).(*Object)
			script := []any{}
			calls := []any{}
			nextID := 0
			now := int64(1793491200000)
			parser, e := jsdate.New("UTC")
			must(t, e)
			options := Options{Authority: assertionauthority.Options{DateParser: parser, Compare: func(a, b string) (int, error) { return strings.Compare(a, b), nil }}, NowMillis: func() int64 { calls = append(calls, obj("op", "now", "value", float64(now))); return now }, ID: func() string {
				nextID++
				id := fmt.Sprintf("id-%d", nextID)
				calls = append(calls, obj("op", "id", "value", id))
				return id
			}}
			flag := func(name string) func(Context) (bool, error) {
				return func(Context) (bool, error) {
					calls = append(calls, obj("op", name, "context", clone(currentContext)))
					return get(flags, name) == true, nil
				}
			}
			options.Enabled = flag("enabled")
			options.LearningEnabled = flag("learningEnabled")
			options.AutoAdmission = flag("autoAdmission")
			current := func(_ Context, ref *Object) (*assertionauthority.Result, error) {
				calls = append(calls, obj("op", "current", "context", clone(currentContext), "reference", clone(ref)))
				v := get(views, str(get(ref, "id")))
				if len(script) > 0 {
					v = script[0]
					script = script[1:]
				}
				return viewValue(clone(v)), nil
			}
			for index, step := range arr(get(input, "steps")) {
				calls = []any{}
				var result any
				var actionError error
				// Trigger DDL is trusted test fixture setup, never a production SQL escape.
				statement := str(get(step, "statement"))
				ddl := str(get(step, "op")) == "sql" && (strings.HasPrefix(statement, "CREATE TRIGGER") || strings.HasPrefix(statement, "DROP TRIGGER"))
				if ddl {
					_, actionError = f.main.Exec(statement)
				} else {
					rollback := errors.New("fixture_rollback")
					txError := f.storage.Transaction(ctx, func(storage *relationstorage.Store) error {
						read, e := storage.Reader()
						if e != nil {
							return e
						}
						c := &candidates{storage: storage, read: read, options: options, current: current}
						rc := contextValue(currentContext)
						insert := func(record, entity *Object, key string) error {
							if entity != nil {
								existing, e := read.Entity(str(get(entity, "id")))
								if e != nil {
									return e
								}
								if existing != nil {
									entity = nil
								}
							}
							return storage.Put(record, key, entity)
						}
						action := func() error {
							switch str(get(step, "op")) {
							case "admit":
								out, e := c.admit(rc, object(get(step, "backing")), arr(get(step, "proposals")))
								if e == nil {
									result = admissionValue(out)
								}
								return e
							case "list":
								o := get(step, "options")
								opts := ListOptions{PersonID: str(get(o, "personId")), Status: str(get(o, "status"))}
								if has(o, "limit") {
									n := num(get(o, "limit"))
									opts.Limit = &n
								}
								out, e := c.list(rc, opts)
								if e == nil {
									result = listValue(out)
								}
								return e
							case "review":
								v, e := c.review(rc, str(get(step, "id")), num(get(step, "version")), str(get(step, "status")), str(get(step, "actorId")))
								if v != nil {
									result = v
								}
								return e
							case "purge", "changed":
								out, e := storage.PurgeAssertion(str(get(step, "id")))
								if e == nil {
									result = stringArray(out)
								}
								return e
							case "seed":
								return insert(object(get(step, "record")), object(get(step, "entity")), str(get(step, "key")))
							case "seedBulk":
								for _, r := range arr(get(step, "records")) {
									if e := insert(object(r), object(get(r, "target")), str(get(r, "id"))); e != nil {
										return e
									}
								}
								return nil
							case "storagePut":
								return storage.Put(object(get(step, "record")), str(get(step, "key")), object(get(step, "entity")))
							case "storageUpdate":
								return storage.Update(object(get(step, "record")), num(get(step, "version")))
							case "sql":
								return storage.WithWriteSession(func(s *recovery.WriteSession) error { _, e := s.Exec(statement); return e })
							case "setView":
								return views.Set(str(get(step, "id")), clone(get(step, "view")))
							case "setCurrentScript":
								script = clone(get(step, "responses")).([]any)
								return nil
							case "flags", "context":
								target := flags
								if get(step, "op") == "context" {
									target = currentContext
								}
								for _, m := range object(get(step, "value")).Members() {
									_ = target.Set(m.Key, clone(m.Value))
								}
								return nil
							case "clock":
								now = int64(num(get(step, "value")))
								return nil
							}
							return fmt.Errorf("unknown action %v", get(step, "op"))
						}
						actionError = action()
						if get(step, "rollback") == true {
							return rollback
						}
						return nil
					})
					if !errors.Is(txError, rollback) {
						must(t, txError)
					}
				}
				actual := new(Object)
				if actionError != nil {
					_ = actual.Set("error", actionError.Error())
				} else if result == nil {
					_ = actual.Set("undefined", true)
				} else {
					_ = actual.Set("result", result)
				}
				_ = actual.Set("calls", calls)
				_ = actual.Set("db", state(t, f.main))
				want := arr(get(input, "trace"))[index]
				if !deepEqual(actual, want) {
					t.Fatalf("step %d %v\ngot %s\nwant %s", index, get(step, "op"), jsonText(t, actual), jsonText(t, want))
				}
			}
		})
	}
}
