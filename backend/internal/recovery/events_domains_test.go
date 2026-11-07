package recovery

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDomainEventValidationMatchesNode(t *testing.T) {
	bytes, err := os.ReadFile("testdata/domain-events-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Input json.RawMessage
		Valid bool
	}
	if err = json.Unmarshal(bytes, &fixtures); err != nil {
		t.Fatal(err)
	}
	for i, fixture := range fixtures {
		err := ValidateEvent(string(fixture.Input))
		if (err == nil) != fixture.Valid {
			t.Errorf("fixture %d accepted=%t want=%t: %v", i, err == nil, fixture.Valid, err)
		}
	}
}
