package webread

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNodeExtractionParity(t *testing.T) {
	data, e := os.ReadFile("testdata/node-extract.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		HTML     string
		Expected Text
	}
	if e = json.Unmarshal(data, &cases); e != nil {
		t.Fatal(e)
	}
	for i, c := range cases {
		got, e := Extract(c.HTML)
		if e != nil || got != c.Expected {
			t.Errorf("case%d got %#v want %#v err %v", i, got, c.Expected, e)
		}
	}
}
