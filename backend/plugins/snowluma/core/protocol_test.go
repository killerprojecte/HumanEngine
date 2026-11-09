package snowluma

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestNodeCQParity(t *testing.T) {
	data, e := os.ReadFile("testdata/node-cq.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Input    string
		Expected []Segment
	}
	if e = json.Unmarshal(data, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		got := ParseCQ(c.Input)
		if !reflect.DeepEqual(got, c.Expected) {
			t.Errorf("%q got %#v want %#v", c.Input, got, c.Expected)
		}
	}
}
func TestOpaqueIDsMatchNodeBytes(t *testing.T) {
	data, e := os.ReadFile("testdata/node-identifiers.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct{ Adapter, Account, Kind, Peer, Platform, SceneID, MessageID, UserID string }
	if e = json.Unmarshal(data, &f); e != nil {
		t.Fatal(e)
	}
	if SceneID(f.Adapter, f.Account, f.Kind, f.Peer) != f.SceneID || MessageID(f.SceneID, f.Platform) != f.MessageID || UserID(f.Adapter, f.Account, f.Platform) != f.UserID {
		t.Fatal("opaque identity changed")
	}
}
