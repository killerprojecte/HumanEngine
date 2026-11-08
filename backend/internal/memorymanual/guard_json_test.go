package memorymanual

import (
	"encoding/json"
	"math"
	"testing"
)

func TestGuardJSONPreservesMissingAndNull(t *testing.T) {
	for _, raw := range []string{`{}`, `{"expectedVersion":null,"expectedDeletionEpoch":null}`} {
		var g Guard
		if e := json.Unmarshal([]byte(raw), &g); e != nil {
			t.Fatal(e)
		}
		if !math.IsNaN(g.ExpectedVersion) || !math.IsNaN(g.ExpectedDeletionEpoch) {
			t.Fatal("missing guard became valid legacy zero")
		}
	}
	var g Guard
	if e := json.Unmarshal([]byte(`{"expectedVersion":0,"expectedDeletionEpoch":0,"idempotencyKey":"x"}`), &g); e != nil || !safe(g.ExpectedVersion) || !safe(g.ExpectedDeletionEpoch) {
		t.Fatal(g, e)
	}
}
