package botruntime

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestActualSourceValidationDependencyMutation(t *testing.T) {
	raw, err := os.ReadFile("../sourceevents/testdata/runtime-authority-faults.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		ValidationCases []struct {
			Mode     string
			Eligible bool
		}
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures.ValidationCases) != 4 {
		t.Fatal("missing actual-source validation probes")
	}
	for _, tc := range fixtures.ValidationCases {
		t.Run(tc.Mode, func(t *testing.T) {
			r, a, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) { t.Error("unexpected model") }))
			scene := fixtureScene(false)
			if err := r.RegisterScene(scene); err != nil {
				t.Fatal(err)
			}
			topic, err := r.Ingest(context.Background(), scene.ID, fixtureMessage(scene.ID, "source", "Synthetic."), false)
			if err != nil {
				t.Fatal(err)
			}
			s := r.state(scene.ID)
			s.mu.Lock()
			turn, err := r.startLocked(s, topic, false, nil)
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			c := turn.context
			if tc.Mode == "recall-local" {
				c.AddDependency("blocked")
			}
			if tc.Mode == "add-recalled" {
				a.mu.Lock()
				a.recalled[scene.ID+"|blocked"] = true
				a.mu.Unlock()
			}
			c.AddValidation(func() bool {
				switch tc.Mode {
				case "add-recalled":
					c.AddDependency("blocked")
				case "abort":
					c.root.Abort(code("synthetic_abort"))
				case "recall-local":
					s.mu.Lock()
					s.recalled = map[string]bool{"blocked": true}
					r.publishLocked(s)
					s.mu.Unlock()
				}
				return true
			})
			got, err := c.Eligible()
			if err != nil || got != tc.Eligible {
				t.Fatalf("eligible=%v err=%v expected=%v", got, err, tc.Eligible)
			}
		})
	}
}
