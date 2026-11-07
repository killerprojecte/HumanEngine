package sourceevents

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type oracle struct {
	Hashes      []struct{ Identity, Metadata, IdentityHash, HeadHash string }
	Validations []struct {
		Schema, Input, Output string
		Valid                 bool
	}
	Traces []struct {
		Name    string
		Options map[string]int
		Steps   []struct{ Op, Result, Error, Callbacks, Rows string }
	}
	Faults []struct {
		Stage         string
		Outer, Caught bool
		Error         *string
		Calls         [][]string
		Head          string
		RevisionCount int
	}
}

func readOracle(t *testing.T) oracle {
	t.Helper()
	b, e := os.ReadFile("testdata/source-events.json")
	if e != nil {
		t.Fatal(e)
	}
	var o oracle
	if e = json.Unmarshal(b, &o); e != nil {
		t.Fatal(e)
	}
	return o
}
func parse(t *testing.T, s string) any {
	t.Helper()
	v, e := jsjson.Parse(s)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func stringify(t *testing.T, v any) string {
	t.Helper()
	s, e := jsjson.Stringify(v)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestActualSourceHashes(t *testing.T) {
	for _, c := range readOracle(t).Hashes {
		i, e := ParseIdentity(parse(t, c.Identity))
		if e != nil {
			t.Fatal(e)
		}
		h, e := IdentityKey(i)
		if e != nil || h != c.IdentityHash {
			t.Fatalf("identity hash: %s %v", h, e)
		}
		m, e := ParseMetadata(parse(t, c.Metadata))
		if e != nil {
			t.Fatal(e)
		}
		h, e = HeadHash(m, nil, nil)
		if e != nil || h != c.HeadHash {
			t.Fatalf("head hash: %s %v", h, e)
		}
	}
}
func TestActualSourceValidation(t *testing.T) {
	for n, c := range readOracle(t).Validations {
		v := parse(t, c.Input)
		var e error
		var out any
		switch c.Schema {
		case "identity":
			x, err := ParseIdentity(v)
			e = err
			out = x.Value()
		case "ref":
			x, err := ParseRef(v)
			e = err
			out = x.Value()
		case "change":
			x, err := ParseChange(v)
			e = err
			out = x.Value()
		default:
			t.Fatal(c.Schema)
		}
		if (e == nil) != c.Valid {
			t.Fatalf("case %d %s: error=%v expected valid=%v", n, c.Schema, e, c.Valid)
		}
		if e == nil && stringify(t, out) != c.Output {
			t.Fatalf("case %d canonical byte order mismatch", n)
		}
	}
}

func TestActualSourceChangeHashBytes(t *testing.T) {
	for _, trace := range readOracle(t).Traces {
		for _, step := range trace.Steps {
			operation := parse(t, step.Op).([]any)
			if operation[0] != "observe" {
				continue
			}
			change, e := ParseChange(operation[1])
			if e != nil || change.Kind != "edit" {
				continue
			}
			result := parse(t, step.Result).(*jsjson.Object)
			if str(result, "status") != "edited" {
				continue
			}
			rows := parse(t, step.Rows).(*jsjson.Object)
			keys, _ := get(rows, "source_event_edit_keys").([]any)
			bound, e := jsjson.UTF8Text(change.EditID)
			if e != nil {
				t.Fatal(e)
			}
			for _, v := range keys {
				row := v.(*jsjson.Object)
				if str(row, "edit_key") != bound {
					continue
				}
				hash, e := ChangeHash(change)
				if e != nil || hash != str(row, "change_hash") {
					t.Fatalf("%s change hash mismatch: %s %v", trace.Name, hash, e)
				}
			}
		}
	}
}
