package sourceevents

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type runtimeProbeResult struct {
	Value json.RawMessage
	Error string
}

func TestActualRuntimeRawRows(t *testing.T) {
	raw, e := os.ReadFile("testdata/runtime-authority.json")
	if e != nil {
		t.Fatal(e)
	}
	var oracle struct {
		Display       struct{ Scene, Identity string }
		MalformedRows []struct {
			Kind, Raw, Message              string
			Participation, Current, Display runtimeProbeResult
		}
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	if len(oracle.MalformedRows) != 30 {
		t.Fatal("missing actual-source raw row probes", len(oracle.MalformedRows))
	}
	f, a := runtimeFixture(t, Options{})
	scene := sceneFromValue(runtimeObject(t, oracle.Display.Scene))
	for i, c := range oracle.MalformedRows {
		t.Run(fmt.Sprintf("%s_%d", c.Kind, i), func(t *testing.T) {
			runtimePut(t, f.main, "scenes", scene.ID, oracle.Display.Scene)
			runtimePut(t, f.main, "config", "identity:"+scene.ID+":speaker", oracle.Display.Identity)
			if c.Kind == "scene" {
				runtimePut(t, f.main, "scenes", scene.ID, c.Raw)
			} else {
				runtimePut(t, f.main, "config", "identity:"+scene.ID+":speaker", c.Raw)
			}
			message := runtimeObject(t, c.Message)
			checkBool := func(label string, expected runtimeProbeResult, value bool, err error) {
				t.Helper()
				if (err != nil) != (expected.Error != "") {
					t.Fatalf("%s error differs: %v / %s", label, err, expected.Error)
				}
				if err == nil {
					var want bool
					if e := json.Unmarshal(expected.Value, &want); e != nil {
						t.Fatal(e)
					}
					if value != want {
						t.Fatal(label, value, want)
					}
				}
			}
			if c.Kind == "scene" {
				value, err := a.SceneParticipationEnabled(scene)
				checkBool("participation", c.Participation, value, err)
				value, err = a.ValidateSources(scene, []*jsjson.Object{message}, nil)
				checkBool("current", c.Current, value, err)
			}
			rows, err := a.ProjectDisplay(scene, []*jsjson.Object{message}, nil)
			if (err != nil) != (c.Display.Error != "") {
				t.Fatal("display error", err, c.Display.Error)
			}
			if err == nil {
				var expected string
				if e := json.Unmarshal(c.Display.Value, &expected); e != nil {
					t.Fatal(e)
				}
				actual, e := jsjson.Stringify(runtimeValues(rows))
				if e != nil || actual != expected {
					t.Fatal("display", actual, expected, e)
				}
			}
		})
	}
}
