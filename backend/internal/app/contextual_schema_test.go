package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
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
		inputSchema := field(definition, "inputSchema").(*jsjson.Object)
		inputSchema.Set("type", "mutated")
		next := contextualToolSchema(snapshot)
		if field(next, "description") == "mutated" || field(field(next, "inputSchema"), "type") == "mutated" {
			t.Fatal("definition leaked between turns")
		}
	}
}

func BenchmarkContextualToolSchema(b *testing.B) {
	snapshot := new(settingsSnapshot)
	snapshot.publish(map[string]any{"memoryGraphEnabled": true, "memoryCommitmentsEnabled": true, "memoryKnowledgeEnabled": true})
	b.ReportAllocs()
	for b.Loop() {
		_ = contextualToolSchema(snapshot)
	}
}
