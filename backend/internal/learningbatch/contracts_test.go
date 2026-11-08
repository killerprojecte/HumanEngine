package learningbatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestActualSourceContracts(t *testing.T) {
	raw, e := os.ReadFile("testdata/contracts.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Sources map[string]string
		Cases   []struct {
			Schema        string
			Input, Output json.RawMessage
			Success       bool
		}
		Signals []struct {
			Text   string
			Result bool
		}
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	for p, want := range fixture.Sources {
		b, e := os.ReadFile(filepath.Join("../../..", p))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != want {
			t.Fatalf("source changed: %s", p)
		}
	}
	s, e := loadSchemas()
	if e != nil {
		t.Fatal(e)
	}
	for i, c := range fixture.Cases {
		v, e := jsjson.ParseBytes(c.Input)
		if e != nil {
			t.Fatal(e)
		}
		actual, e := s.parse(v.(*Object), c.Schema)
		if (e == nil) != c.Success {
			t.Fatalf("case %d %s success %v expected %v: %v", i, c.Schema, e == nil, c.Success, e)
		}
		if e == nil {
			expected, e := jsjson.ParseBytes(c.Output)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(native(actual), native(expected)) {
				t.Fatalf("case %d differs", i)
			}
		}
	}
	for _, c := range fixture.Signals {
		if got := ExplicitDurableSignal(c.Text); got != c.Result {
			t.Fatalf("signal %q=%v want %v", c.Text, got, c.Result)
		}
	}
}
func TestSearchSliceUsesUTF16NotRunes(t *testing.T) {
	got := sliceUTF16("a😀b", 2)
	u, e := jsjson.UTF16(got)
	if e != nil || len(u) != 2 || u[0] != 'a' || u[1] != 0xd83d {
		t.Fatalf("slice %x %v", u, e)
	}
}
