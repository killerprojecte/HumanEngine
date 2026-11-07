package recovery

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"testing"
)

func TestActualNodeLosslessEventAcceptanceAndTypedBytes(t *testing.T) {
	raw, err := os.ReadFile("testdata/lossless-events-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Raw      string
			Accepted bool
			Payload  *string
			Before   Head
			After    *Head
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	f := newWriterFixture(t)
	for i, entry := range fixture.Cases {
		if accepted := ValidateEvent(entry.Raw) == nil; accepted != entry.Accepted {
			t.Fatalf("case%d acceptance %v != %v", i, accepted, entry.Accepted)
		}
		decoded, err := jsjson.Parse(entry.Raw)
		if err != nil {
			t.Fatal(err)
		}
		event := validationValue(decoded).(map[string]any)
		var typed Revocation
		switch event["kind"] {
		case "source":
			typed = Source{SceneID: event["sceneId"].(string), MessageID: event["messageId"].(string)}
		case "habit_delete":
			typed = HabitDelete{ID: event["id"].(string), Fingerprint: event["fingerprint"].(string)}
		case "scene_flags":
			flags := map[string]bool{}
			for key, value := range event["flags"].(map[string]any) {
				flags[key] = value.(bool)
			}
			typed = SceneFlags{SchemaVersion: 2, SceneID: event["sceneId"].(string), IdentityHash: event["identityHash"].(string), Flags: flags}
		case "assertion_eligibility":
			changes := []EligibilityChange{}
			for _, raw := range event["changes"].([]any) {
				change := raw.(map[string]any)
				changes = append(changes, EligibilityChange{ID: change["id"].(string), IncarnationHash: change["incarnationHash"].(string), State: EligibilityState{Status: "invalidated"}})
			}
			typed = AssertionEligibility{OperationID: event["operationId"].(string), Mode: "transition", Changes: changes}
		default:
			t.Fatal("unmapped fixture")
		}
		encoded, err := encodeRevocation(typed)
		if !entry.Accepted {
			if err == nil {
				t.Fatalf("case%d invalid typed accepted", i)
			}
			continue
		}
		if err != nil || entry.Payload == nil || encoded.payload != *entry.Payload {
			t.Fatalf("case%d payload differs: %v", i, err)
		}
		after, err := f.authority.Append(context.Background(), typed, entry.Before, false)
		if err != nil || entry.After == nil || after != *entry.After {
			t.Fatalf("case%d hash differs %+v != %+v: %v", i, after, entry.After, err)
		}
	}
}
