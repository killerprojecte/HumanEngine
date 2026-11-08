package memoryservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorymanual"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type manualSnapshot map[string][]map[string]any
type manualStep struct {
	Operation, Error string
	Args             []json.RawMessage
	Result           json.RawMessage
	Before, After    manualSnapshot
}

var manualTables = []string{"scenes", "recall_tombstones", "memories", "memory_sources", "memory_supports", "memory_deleted", "memory_assertion_state", "memory_assertion_revisions", "memory_assertion_revision_sources", "memory_assertion_revision_parents", "memory_command_receipts"}

func manualFixed(t *testing.T) (*Service, scopedFixture) {
	t.Helper()
	s, f := serviceSetup(t)
	st, e := assertions.New(f.main, f.recovery, assertions.Options{Now: func() string { return "2026-10-28T12:00:00.000Z" }})
	if e != nil {
		t.Fatal(e)
	}
	s.d.Assertions = st
	s, e = New(s.d)
	if e != nil {
		t.Fatal(e)
	}
	f.store = st
	if e = st.Transaction(context.Background(), func(st *assertions.Store) error { return st.EnsureManualCommands() }); e != nil {
		t.Fatal(e)
	}
	return s, f
}
func manualSeed(t *testing.T, f scopedFixture, snapshot manualSnapshot) {
	t.Helper()
	for i := len(manualTables) - 1; i >= 0; i-- {
		if _, e := f.main.Exec("DELETE FROM " + manualTables[i]); e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range manualTables {
		for _, row := range snapshot[table] {
			keys := []string{}
			for k := range row {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			args := []any{}
			marks := []string{}
			for _, k := range keys {
				args = append(args, row[k])
				marks = append(marks, "?")
			}
			if _, e := f.main.Exec("INSERT INTO "+table+"("+strings.Join(keys, ",")+") VALUES("+strings.Join(marks, ",")+")", args...); e != nil {
				t.Fatalf("seed %s: %v", table, e)
			}
		}
	}
}
func manualSnapshotDB(t *testing.T, db *sql.DB) manualSnapshot {
	t.Helper()
	out := manualSnapshot{}
	for _, table := range manualTables {
		rows, e := db.Query("SELECT * FROM " + table)
		if e != nil {
			t.Fatal(e)
		}
		cols, e := rows.Columns()
		if e != nil {
			t.Fatal(e)
		}
		out[table] = []map[string]any{}
		for rows.Next() {
			cells := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range cells {
				ptrs[i] = &cells[i]
			}
			if e = rows.Scan(ptrs...); e != nil {
				t.Fatal(e)
			}
			row := map[string]any{}
			for i, k := range cols {
				switch v := cells[i].(type) {
				case int64:
					row[k] = float64(v)
				case []byte:
					row[k] = string(v)
				default:
					row[k] = v
				}
			}
			out[table] = append(out[table], row)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		if e = rows.Close(); e != nil {
			t.Fatal(e)
		}
	}
	return out
}
func manualCompare(t *testing.T, got, want manualSnapshot) {
	t.Helper()
	encode := func(rows []map[string]any) []string {
		out := []string{}
		for _, row := range rows {
			b, e := json.Marshal(row)
			if e != nil {
				t.Fatal(e)
			}
			out = append(out, string(b))
		}
		sort.Strings(out)
		return out
	}
	for _, table := range manualTables {
		if !reflect.DeepEqual(encode(got[table]), encode(want[table])) {
			t.Fatalf("%s differs\ngot %v\nwant %v", table, encode(got[table]), encode(want[table]))
		}
	}
}
func TestManualActualSourcePrestate(t *testing.T) {
	raw, e := os.ReadFile("../memorymanual/testdata/manual-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var data struct {
		Runtime     json.RawMessage
		RuntimeHash string
		Sources     map[string]string
		Cases       []struct {
			Name  string
			Steps []manualStep
		}
	}
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	var compact bytes.Buffer
	if e = json.Compact(&compact, data.Runtime); e != nil {
		t.Fatal(e)
	}
	if data.RuntimeHash != "b5702f593fcc596fff0f5163b361c56ace9917ff87efe76558370e3270bf4f4d" || fmt.Sprintf("%x", sha256.Sum256(compact.Bytes())) != data.RuntimeHash {
		t.Fatal("runtime provenance differs")
	}
	if len(data.Sources) != 16 {
		t.Fatalf("source provenance set: %d", len(data.Sources))
	}
	for path, want := range data.Sources {
		b, e := os.ReadFile(filepath.Join("../../..", path))
		if e != nil {
			t.Fatal(e)
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != want {
			t.Fatalf("source drift %s", path)
		}
	}
	total, boundaries := 0, 0
	for _, scenario := range data.Cases {
		for index, step := range scenario.Steps {
			if step.Operation == "propose" {
				boundaries++
				continue
			}
			total++
			t.Run(fmt.Sprintf("%s/%d-%s", scenario.Name, index, step.Operation), func(t *testing.T) {
				s, f := manualFixed(t)
				manualSeed(t, f, step.Before)
				id := ""
				if len(step.Result) > 0 {
					v, _ := jsjson.Parse(string(step.Result))
					if o, ok := v.(*Object); ok {
						id = text(o, "id")
					}
				}
				command, e := s.manualCommands(memorymanual.Runtime{ID: func() (string, error) {
					if id == "" {
						return "invalid-create-id", nil
					}
					return id, nil
				}, Now: func() string { return "2026-10-28T12:00:00.000Z" }})
				if e != nil {
					t.Fatal(e)
				}
				argObject := func(i int) *Object { return parseObject(t, string(step.Args[i])) }
				argString := func(i int) string {
					var v string
					if e := json.Unmarshal(step.Args[i], &v); e != nil {
						t.Fatal(e)
					}
					return v
				}
				var result any
				ctx := context.Background()
				switch step.Operation {
				case "create":
					result, e = command.Create(ctx, argObject(0))
				case "update":
					result, e = command.Update(ctx, argString(0), argObject(1))
				case "lock":
					var locked bool
					if err := json.Unmarshal(step.Args[1], &locked); err != nil {
						t.Fatal(err)
					}
					result, e = command.Lock(ctx, argString(0), locked)
				case "updateGuarded":
					var g memorymanual.Guard
					if err := json.Unmarshal(step.Args[2], &g); err != nil {
						t.Fatal(err)
					}
					result, e = command.UpdateGuarded(ctx, argString(0), argObject(1), g)
				case "promotePublic":
					in := argObject(0)
					simulation, _ := value(in, "simulation").(bool)
					result, e = command.PromotePublic(ctx, memorymanual.Context{SceneID: text(in, "sceneId"), Simulation: simulation}, argString(1))
				default:
					t.Fatalf("unknown manual operation %s", step.Operation)
				}
				if step.Error != "" {
					if e == nil || (!strings.Contains(e.Error(), step.Error)) {
						t.Fatalf("got %v want %s", e, step.Error)
					}
				} else {
					if e != nil {
						t.Fatal(e)
					}
					var encoded string
					switch r := result.(type) {
					case *Object:
						encoded, e = jsjson.Stringify(r)
					case memorymanual.Result:
						o := new(Object)
						_ = o.Set("status", r.Status)
						if r.Reason != "" {
							_ = o.Set("reason", r.Reason)
						}
						if r.Memory != nil {
							_ = o.Set("memory", r.Memory)
						}
						encoded, e = jsjson.Stringify(o)
					}
					if e != nil {
						t.Fatal(e)
					}
					var a, b any
					if e = json.Unmarshal([]byte(encoded), &a); e != nil {
						t.Fatal(e)
					}
					if e = json.Unmarshal(step.Result, &b); e != nil {
						t.Fatal(e)
					}
					if !reflect.DeepEqual(a, b) {
						t.Fatalf("result differs\ngot %s\nwant %s", encoded, step.Result)
					}
				}
				manualCompare(t, manualSnapshotDB(t, f.main), step.After)
			})
		}
	}
	if total != 57 || boundaries != 1 {
		t.Fatalf("coverage %d manual / %d setup", total, boundaries)
	}
}
