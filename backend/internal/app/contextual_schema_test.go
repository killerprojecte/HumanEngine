package app

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"reflect"
	"testing"
)

func TestContextualActualSourceDefinitions(t *testing.T) {
	var oracle struct{ Definitions []json.RawMessage }
	if err := json.Unmarshal([]byte(contextualToolsSource), &oracle); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Definitions) != 8 {
		t.Fatal("missing source flag combinations")
	}
	for flags, wantRaw := range oracle.Definitions {
		snapshot := new(settingsSnapshot)
		snapshot.publish(map[string]any{"memoryGraphEnabled": flags&1 != 0, "memoryCommitmentsEnabled": flags&2 != 0, "memoryKnowledgeEnabled": flags&4 != 0})
		definition := contextualToolSchema(snapshot)
		actual, err := jsjson.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err = json.Unmarshal(actual, &got); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(wantRaw, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("source definition flags=%d", flags)
		}
		if field(definition, "name") != "learning.propose" {
			t.Fatal("wrong tool")
		}
		// A caller cannot poison a future turn's freshly built advertisement.
		definition.Set("description", "mutated")
		if field(contextualToolSchema(snapshot), "description") == "mutated" {
			t.Fatal("definition leaked between turns")
		}
	}
}
