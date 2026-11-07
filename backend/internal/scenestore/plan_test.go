package scenestore

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestActualStoreSceneWritePolicy(t *testing.T) {
	raw, err := os.ReadFile("testdata/write.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Name            string
			Previous, Input json.RawMessage
			Output          *string
			Events          []string
			Error           string
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, entry := range fixture.Cases {
		t.Run(entry.Name, func(t *testing.T) {
			parse := func(raw []byte) *jsjson.Object {
				v, e := jsjson.ParseBytes(raw)
				if e != nil {
					t.Fatal(e)
				}
				if v == nil {
					return nil
				}
				return v.(*jsjson.Object)
			}
			previous, input := parse(entry.Previous), parse(entry.Input)
			before, _ := jsjson.Stringify(input)
			plan, err := Prepare(previous, input)
			after, _ := jsjson.Stringify(input)
			if before != after {
				t.Fatal("input mutated")
			}
			if entry.Error != "" {
				if err == nil || err.Error() != entry.Error {
					t.Fatalf("want %s got %v", entry.Error, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			output, err := jsjson.Stringify(plan.Next)
			if err != nil || entry.Output == nil || output != *entry.Output {
				t.Fatalf("output=%s want=%v error=%v", output, entry.Output, err)
			}
			if len(entry.Events) == 0 {
				if plan.Intent != nil {
					t.Fatal("unexpected revocation")
				}
				return
			}
			if len(entry.Events) != 1 || plan.Intent == nil {
				t.Fatal("missing revocation")
			}
			var event struct {
				Kind          string
				SchemaVersion int
				SceneID       string
				IdentityHash  string
				Flags         map[string]bool
			}
			if err = json.Unmarshal([]byte(entry.Events[0]), &event); err != nil {
				t.Fatal(err)
			}
			if event.Kind != "scene_flags" || event.SchemaVersion != plan.Intent.SchemaVersion || event.SceneID != plan.Intent.SceneID || event.IdentityHash != plan.Intent.IdentityHash {
				t.Fatalf("intent mismatch: %+v %+v", event, plan.Intent)
			}
			for _, key := range flagKeys {
				if event.Flags[key] != plan.Intent.Flags[key] {
					t.Fatal("flag mismatch", key)
				}
			}
		})
	}
}
