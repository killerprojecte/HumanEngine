package memoryread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"path/filepath"
	"testing"
)

func oracleGet(v any, k string) any {
	o, _ := v.(*jsjson.Object)
	if o == nil {
		return nil
	}
	x, _ := o.Get(k)
	return x
}
func oracleJSON(t *testing.T, v any) string {
	t.Helper()
	s, e := jsjson.Stringify(v)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestActualSourceMemoryList(t *testing.T) {
	raw, e := os.ReadFile("testdata/source-oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	root := parseObject(t, string(raw))
	for _, m := range oracleGet(root, "sources").(*jsjson.Object).Members() {
		b, e := os.ReadFile(filepath.Join("../../..", m.Key))
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != m.Value {
			t.Fatalf("source drift %s", m.Key)
		}
	}
	for _, v := range oracleGet(root, "scenarios").([]any) {
		t.Run(oracleGet(v, "name").(string), func(t *testing.T) {
			f := scopedSetup(t)
			for _, row := range oracleGet(v, "rows").([]any) {
				_, e := f.main.Exec("INSERT INTO memories(id,scope_key,person_id,content,value,fingerprint) VALUES(?,?,?,?,?,?)", oracleGet(row, "id"), oracleGet(row, "scope_key"), oracleGet(row, "person_id"), oracleGet(row, "content"), oracleGet(row, "value"), oracleGet(row, "fingerprint"))
				if e != nil {
					t.Fatal(e)
				}
			}
			for _, m := range oracleGet(v, "proofs").(*jsjson.Object).Members() {
				if _, e := f.main.Exec("INSERT INTO memory_supports(memory_id,value) VALUES(?,?)", m.Key, oracleJSON(t, m.Value)); e != nil {
					t.Fatal(e)
				}
			}
			for _, id := range oracleGet(v, "recalled").([]any) {
				if _, e := f.main.Exec("INSERT INTO recall_tombstones VALUES('s',?,'2000-01-01')", id); e != nil {
					t.Fatal(e)
				}
			}
			if oracleGet(v, "notReady") == true {
				f.store, e = assertions.New(f.main, f.recovery, assertions.Options{AssertReady: func() error { return errors.New("synthetic_not_ready") }})
				if e != nil {
					t.Fatal(e)
				}
			}
			for i, op := range oracleGet(v, "ops").([]any) {
				in := Input{SceneID: oracleGet(op, "sceneId").(string), Simulation: oracleGet(op, "simulation") == true}
				in.PersonID, _ = oracleGet(op, "personId").(string)
				if ids, ok := oracleGet(op, "ids").([]any); ok {
					in.IDs = []string{}
					for _, id := range ids {
						in.IDs = append(in.IDs, id.(string))
					}
				}
				var result []*assertions.Object
				e := f.store.Read(context.Background(), func(r *assertions.Reader) error {
					s, e := New(r, testOptions(t))
					if e != nil {
						return e
					}
					result, e = s.List(in)
					return e
				})
				expected := oracleGet(v, "results").([]any)[i]
				if want := oracleGet(expected, "error"); want != nil {
					if e == nil || e.Error() != want {
						t.Fatalf("op %d error %v want %v", i, e, want)
					}
					continue
				}
				if e != nil {
					t.Fatalf("op %d: %v", i, e)
				}
				values := []any{}
				for _, r := range result {
					values = append(values, r)
				}
				if got, want := oracleJSON(t, values), oracleJSON(t, oracleGet(expected, "value")); got != want {
					t.Fatalf("op %d\ngot %s\nwant %s", i, got, want)
				}
			}
		})
	}
}
