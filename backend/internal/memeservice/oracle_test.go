package memeservice

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"reflect"
	"testing"
)

func TestPinnedTypeScriptDifferential(t *testing.T) {
	b, e := os.ReadFile("testdata/oracle.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Tags []struct {
			Name          string
			Input, Result json.RawMessage
			Error         string
		}
		Concepts []struct {
			Input  string
			Result []string
		}
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	for _, c := range fixture.Tags {
		t.Run(c.Name, func(t *testing.T) {
			v, e := jsjson.Parse(string(c.Input))
			if e != nil {
				t.Fatal(e)
			}
			out, e := ValidateTags(v)
			if c.Error != "" {
				if e == nil || e.Error() != c.Error {
					t.Fatal(e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			expected, e := jsjson.Parse(string(c.Result))
			if e != nil {
				t.Fatal(e)
			}
			a, _ := jsjson.Stringify(out)
			b, _ := jsjson.Stringify(expected)
			if a != b {
				t.Fatalf("%s != %s", a, b)
			}
		})
	}
	for _, c := range fixture.Concepts {
		out, e := concepts(c.Input)
		if e != nil || !reflect.DeepEqual(out, c.Result) {
			t.Fatalf("%q: %v %v expected %v", c.Input, out, e, c.Result)
		}
	}
}
