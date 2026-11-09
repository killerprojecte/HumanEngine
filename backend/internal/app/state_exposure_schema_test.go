package app

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"reflect"
	"testing"
)

func TestStateExposureActualSourceDefinitions(t *testing.T) {
	raw, err := os.ReadFile("testdata/state-tools-oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Combinations []struct {
			Settings map[string]any
			Tools    json.RawMessage
		}
		ISO []struct {
			Value string
			Valid bool
		}
	}
	if err = json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	for _, entry := range oracle.Combinations {
		snapshot := new(settingsSnapshot)
		snapshot.publish(entry.Settings)
		tools := enabledStateExposureSchemas(snapshot)
		actual, err := jsjson.Marshal(memoryToolArray(tools))
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err = json.Unmarshal(actual, &got); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(entry.Tools, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("source definitions differ for %v\ngot %s\nwant %s", entry.Settings, actual, entry.Tools)
		}
	}
	for _, entry := range oracle.ISO {
		if got := stateISODate(entry.Value); got != entry.Valid {
			t.Fatalf("source ISO mismatch %q got %v want %v", entry.Value, got, entry.Valid)
		}
	}
}
