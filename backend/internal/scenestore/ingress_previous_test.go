package scenestore

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"testing"
)

func TestActualIngressFalsyPreviousScene(t *testing.T) {
	raw, err := os.ReadFile("../sourceevents/testdata/ingress-routing.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Spec     struct{ Name, RawScene string }
			Expected struct {
				PersistedScene string
				Error          *string
			}
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, c := range fixture.Cases {
		if c.Spec.Name != "raw-null-scene" && c.Spec.Name != "raw-false-scene" && c.Spec.Name != "raw-zero-scene" && c.Spec.Name != "raw-empty-scene" {
			continue
		}
		seen++
		t.Run(c.Spec.Name, func(t *testing.T) {
			if c.Expected.Error != nil {
				t.Fatal("source did not recreate scene", *c.Expected.Error)
			}
			decoded, err := jsjson.Parse(c.Expected.PersistedScene)
			if err != nil {
				t.Fatal(err)
			}
			next := decoded.(*jsjson.Object)
			id, _ := next.Get("id")
			store := fixtureStore(t)
			if _, err = store.Main.Exec("INSERT INTO scenes(id,value) VALUES(?,?)", id, c.Spec.RawScene); err != nil {
				t.Fatal(err)
			}
			head, err := store.Save(context.Background(), next)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if err = store.Main.QueryRow("SELECT value FROM scenes WHERE id=?", id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != c.Expected.PersistedScene || head.Sequence != 0 {
				t.Fatalf("scene differs or invented revocation: %s / %+v", got, head)
			}
		})
	}
	if seen != 4 {
		t.Fatal("missing actual falsy scene probes")
	}
}
