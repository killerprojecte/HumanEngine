package credentials

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"testing"
)

func TestFirstSaveModelsCreatesSecurityStateInTransaction(t *testing.T) {
	for _, secret := range []bool{false, true} {
		name := "models-only"
		if secret {
			name = "models-and-secret"
		}
		t.Run(name, func(t *testing.T) {
			s := secretFixtureStore(t, false)
			ctx := context.Background()
			parsed, e := jsjson.Parse(`{"providers":[{"id":"p","name":"Synthetic","protocol":"openai-chat","baseUrl":"https://synthetic.invalid"}],"models":[],"routes":[]}`)
			if e != nil {
				t.Fatal(e)
			}
			var mutations *jsjson.Object
			if secret {
				v, _ := jsjson.Parse(`{"p":{"action":"set","value":"synthetic-first-secret"}}`)
				mutations = v.(*jsjson.Object)
			}
			before, e := s.authority.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.SaveModels(ctx, parsed.(*jsjson.Object), mutations); e != nil {
				t.Fatal(e)
			}
			after, e := s.authority.Head(ctx)
			if e != nil {
				t.Fatal(e)
			}
			want := before.Sequence + 1
			if secret {
				want++
			}
			if after.Sequence != want {
				t.Fatalf("intent count %d want%d", after.Sequence, want)
			}
			var version, again int
			if e = s.main.QueryRow("PRAGMA schema_version").Scan(&version); e != nil {
				t.Fatal(e)
			}
			if _, e = s.SaveModels(ctx, parsed.(*jsjson.Object), nil); e != nil {
				t.Fatal(e)
			}
			if e = s.main.QueryRow("PRAGMA schema_version").Scan(&again); e != nil || again != version {
				t.Fatal("repeat mutated schema", e)
			}
			if secret {
				value, ok, e := s.Get(ctx, "model", "p")
				if e != nil || !ok || value != "synthetic-first-secret" {
					t.Fatal("first encrypted secret not readable", ok, e)
				}
			}
		})
	}
}
