package assertions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	_ "github.com/mattn/go-sqlite3"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	main      *sql.DB
	authority *recovery.Authority
	store     *Store
}

func newFixture(t *testing.T, initialize ...bool) fixture {
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
	main, e := sql.Open("sqlite3", filepath.Join(root, "live", "main.sqlite")+"?_foreign_keys=on")
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
	s, e := New(main, a, Options{Now: func() string { return "2026-10-28T00:00:00.000Z" }})
	if e != nil {
		t.Fatal(e)
	}
	if len(initialize) == 0 || initialize[0] {
		if e = s.Initialize(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() { a.Close(); main.Close() })
	return fixture{main, a, s}
}
func parse(t *testing.T, raw string) any {
	t.Helper()
	v, e := jsjson.Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func resultJSON(t *testing.T, v any) string {
	t.Helper()
	switch x := v.(type) {
	case *Object:
		if x == nil {
			return "null"
		}
		s, e := jsjson.Stringify(x)
		if e != nil {
			t.Fatal(e)
		}
		return s
	case []*Object:
		a := []any{}
		for _, o := range x {
			a = append(a, o)
		}
		s, e := jsjson.Stringify(a)
		if e != nil {
			t.Fatal(e)
		}
		return s
	case HistoryPage:
		a := []any{}
		for _, o := range x.Revisions {
			a = append(a, o)
		}
		s, e := jsjson.Stringify(obj("revisions", a, "hasMore", x.HasMore, "unknownRecordedTime", x.UnknownRecordedTime, "retentionIncomplete", x.RetentionIncomplete))
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}
func fixtureInput(t *testing.T, i int) *Object {
	t.Helper()
	o := parse(t, `{"record":{"id":"assertion","scope":{"kind":"scene","sceneId":"scene","personId":"person"},"content":"Synthetic preference","kind":"claim","sources":[{"sceneId":"scene","messageId":"message","senderId":"person"}],"importance":"medium","confidence":0.6,"learnedAt":"2026-01-02T00:00:00.000Z","status":"active","locked":false,"origin":"learned","lineageStatus":"unverified"},"supports":[{"sources":[{"sceneId":"scene","messageId":"message","senderId":"person"}],"parents":["parent"]},{"sources":[{"sceneId":"scene","messageId":"independent","senderId":"person"}],"parents":[]}],"expectedVersion":0,"expectedDeletionEpoch":0,"idempotencyKey":"first","recordedAt":"2026-01-02T00:00:00.000Z","validFrom":null,"validTo":null,"eventKind":"learned_assertion"}`).(*Object)
	if i >= 0 {
		_ = o.Set("expectedVersion", float64(i))
		_ = o.Set("idempotencyKey", fmt.Sprintf("bulk:%d", i))
		day := 2
		if i >= 5 {
			day = 3
		}
		_ = o.Set("recordedAt", fmt.Sprintf("2026-01-%02dT00:00:00.000Z", day))
		_ = get(o, "record").(*Object).Set("content", fmt.Sprintf("Synthetic %d", i))
	}
	return o
}
func invoke(t *testing.T, s *Store, op []any) (out any, err error) {
	t.Helper()
	method := op[0].(string)
	args := op[1:]
	switch method {
	case "append":
		return s.Append(args[0].(*Object))
	case "importLegacy":
		return s.ImportLegacy(args[0].(*Object), args[1].([]any))
	case "purge":
		return s.Purge(args[0].(string))
	case "erase":
		return s.Erase(args[0].(string))
	case "redactSource":
		return s.RedactSource(args[0].(string), args[1].(string))
	case "raw":
		for _, statement := range strings.Split(args[0].(string), ";") {
			if strings.TrimSpace(statement) != "" {
				if e := s.exec(statement); e != nil {
					return nil, e
				}
			}
		}
		return nil, nil
	case "bulk":
		for i := 0; i < int(args[0].(float64)); i++ {
			if _, e := s.Append(fixtureInput(t, i)); e != nil {
				return nil, e
			}
		}
		return nil, nil
	case "rollback":
		if _, e := s.Append(args[0].(*Object)); e != nil {
			return nil, e
		}
		return nil, errors.New("synthetic_rollback")
	case "appendDetached":
		input := args[0].(*Object)
		record := get(input, "record").(*Object)
		_ = record.Set("extra", obj("nan", math.NaN(), "minusZero", math.Copysign(0, -1), "nested", obj("value", "original")))
		value, e := s.Append(input)
		if e != nil {
			return nil, e
		}
		returned := get(value, "record").(*Object)
		alias := returned == record || get(array(get(value, "supports"))[0], "sources").([]any)[0] == get(array(get(input, "supports"))[0], "sources").([]any)[0]
		_ = get(get(returned, "extra"), "nested").(*Object).Set("value", "return mutation")
		nested := get(get(get(record, "extra"), "nested"), "value")
		_ = record.Set("content", "input mutation")
		zero, _ := get(get(returned, "extra"), "minusZero").(float64)
		return obj("aliases", alias, "inputNested", nested, "returnedContent", get(returned, "content"), "nanIsNull", get(get(returned, "extra"), "nan") == nil, "minusZero", math.Signbit(zero)), nil
	case "mutateReturned":
		o, e := s.Append(args[0].(*Object))
		if e == nil {
			_ = get(o, "record").(*Object).Set("content", "CALLER_MUTATION")
		}
		return nil, e
	}
	r := s.reader()
	switch method {
	case "state":
		return r.State(args[0].(string))
	case "revision":
		return r.Revision(args[0].(string), args[1].(float64), args[2].(string))
	case "revisions":
		var limit *float64
		if len(args) > 2 {
			n := args[2].(float64)
			limit = &n
		}
		return r.Revisions(args[0].(string), args[1].(string), limit)
	case "latest":
		return r.LatestRevision(args[0].(string), args[1].(string))
	case "retainedCount":
		return r.RetainedCount(args[0].(string), args[1].(string))
	case "queryRetained":
		return r.QueryRetained(args[0].(string), args[1].(string), args[2].(*Object))
	case "dependents":
		return r.Dependents(args[0].(string))
	}
	return nil, fmt.Errorf("unknown operation %s", method)
}
func snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	all := new(Object)
	for _, name := range []string{"memory_assertion_state", "memory_assertion_revisions", "memory_assertion_revision_sources", "memory_assertion_revision_parents"} {
		rows, e := db.Query("SELECT * FROM " + name + " ORDER BY 1,2")
		if e != nil {
			t.Fatal(e)
		}
		columns, e := rows.Columns()
		if e != nil {
			t.Fatal(e)
		}
		array := []any{}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(values))
			for i := range values {
				dest[i] = &values[i]
			}
			if e = rows.Scan(dest...); e != nil {
				t.Fatal(e)
			}
			o := new(Object)
			for i, k := range columns {
				v := values[i]
				if n, ok := v.(int64); ok {
					v = float64(n)
				}
				_ = o.Set(k, v)
			}
			array = append(array, o)
		}
		if e = errors.Join(rows.Err(), rows.Close()); e != nil {
			t.Fatal(e)
		}
		_ = all.Set(name, array)
	}
	s, e := jsjson.Stringify(all)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestActualAssertionHistoryTransitions(t *testing.T) {
	var oracle struct {
		Traces []struct {
			Name  string
			Steps []struct{ Op, Result, Error, RowsHash string }
		}
	}
	raw, e := os.ReadFile("testdata/history.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	for _, trace := range oracle.Traces {
		t.Run(trace.Name, func(t *testing.T) {
			f := newFixture(t)
			for n, step := range trace.Steps {
				op := parse(t, step.Op).([]any)
				var out any
				var err error
				if op[0] == "noTransaction" {
					out, err = invoke(t, f.store, op[1:])
				} else {
					err = f.store.Transaction(context.Background(), func(b *Store) error { var e error; out, e = invoke(t, b, op); return e })
				}
				if (err != nil) != (step.Error != "") || (err != nil && err.Error() != step.Error) {
					t.Fatalf("step %d %v error=%v want=%s", n, op[0], err, step.Error)
				}
				if err == nil {
					actual := resultJSON(t, out)
					if actual != step.Result {
						t.Fatalf("step %d result=%s want=%s", n, actual, step.Result)
					}
				}
				if got := digest(snapshot(t, f.main)); got != step.RowsHash {
					t.Fatalf("step %d %v complete SQLite row hash=%s want=%s", n, op[0], got, step.RowsHash)
				}
			}
		})
	}
}

func TestReaderConstructionAndExpiredScopes(t *testing.T) {
	f := newFixture(t, false)
	var retained *Reader
	err := f.store.Read(context.Background(), func(r *Reader) error { retained = r; _, e := r.State("missing"); return e })
	if err == nil {
		t.Fatal("missing schema was silently created or hidden")
	}
	var count int
	if e := f.main.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'memory_%' OR name='memories'").Scan(&count); e != nil || count != 0 {
		t.Fatalf("reader created storage count=%d err=%v", count, e)
	}
	if _, e := retained.State("missing"); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped reader=%v", e)
	}
	if e := f.store.Initialize(context.Background()); e != nil {
		t.Fatal(e)
	}
	var escaped *Store
	if e := f.store.Transaction(context.Background(), func(root *Store) error {
		return root.Transaction(context.Background(), func(child *Store) error { escaped = child; return nil })
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := escaped.Append(fixtureInput(t, -1)); !errors.Is(e, sql.ErrTxDone) {
		t.Fatalf("escaped nested writer=%v", e)
	}
}

type countHistoryRows struct {
	Rows
	count *int
}

func (r *countHistoryRows) Next() bool {
	ok := r.Rows.Next()
	if ok {
		*r.count++
	}
	return ok
}
func TestLatestHistoryReadsOnlyNeededSuffix(t *testing.T) {
	f := newFixture(t)
	if e := f.store.Transaction(context.Background(), func(s *Store) error {
		for i := 0; i < 205; i++ {
			if _, e := s.Append(fixtureInput(t, i)); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	for _, invalid := range []int{0, 2} {
		if invalid != 0 {
			if _, e := f.main.Exec("UPDATE memory_assertion_revisions SET value='{invalid' WHERE version>203"); e != nil {
				t.Fatal(e)
			}
		}
		if e := f.store.Read(context.Background(), func(r *Reader) error {
			query := r.query
			count := 0
			r.query = func(q string, args ...any) (Rows, error) {
				rows, e := query(q, args...)
				if e != nil {
					return nil, e
				}
				if strings.Contains(q, "ORDER BY version DESC") {
					return &countHistoryRows{rows, &count}, nil
				}
				return rows, nil
			}
			latest, e := r.LatestRevision("assertion", "scene:scene")
			if e != nil {
				return e
			}
			if get(latest, "version") != float64(205-invalid) || count != invalid+1 {
				t.Fatalf("latest=%v decoded rows=%d", get(latest, "version"), count)
			}
			all, e := r.Revisions("assertion", "scene:scene", nil)
			if e != nil {
				return e
			}
			same, e := equalJSON(latest, all[len(all)-1])
			if e != nil {
				return e
			}
			if !same {
				t.Fatal("latest differs from full source projection")
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
}
