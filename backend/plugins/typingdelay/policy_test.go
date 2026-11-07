package typingdelay

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNodeGraphemeAndTimingParity(t *testing.T) {
	data, err := os.ReadFile("testdata/node-delay.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Segment  Segment
		Config   json.RawMessage
		Expected int
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		got, err := Delay(c.Segment, c.Config)
		if err != nil || got != c.Expected {
			t.Fatalf("case %d: got %d want %d: %v", i, got, c.Expected, err)
		}
	}
	if _, err = Delay(Segment{}, json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown config accepted")
	}
}
