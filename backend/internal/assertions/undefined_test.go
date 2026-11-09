package assertions

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"testing"
)

func TestEligibilityUndefinedActualSourceOracle(t *testing.T) {
	raw, err := os.ReadFile("../jsjson/testdata/native-undefined.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Record   json.RawMessage
		Material string
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	v, err := jsjson.Parse(string(fixture.Record))
	if err != nil {
		t.Fatal(err)
	}
	record := v.(*Object)
	_ = record.Set("supersedesId", jsjson.Undefined)
	hash, err := EligibilityMaterial(record, []any{jsjson.Undefined, nil})
	if err != nil || hash != fixture.Material {
		t.Fatal(hash, fixture.Material, err)
	}
}
