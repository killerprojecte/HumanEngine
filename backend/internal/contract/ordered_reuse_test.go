package contract

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sync"
	"testing"
)

func TestConcurrentParseOrderedReusesImmutableDefinitions(t *testing.T) {
	s, e := NewSchemas()
	if e != nil {
		t.Fatal(e)
	}
	definitions := s.orderedDefinitions
	raw := []byte(`{"providers":[{"id":"p","name":"Synthetic","protocol":"openai-chat","baseUrl":"https://synthetic.invalid"}],"models":[],"routes":[]}`)
	reference, e := s.ParseOrdered("ModelConfig", raw)
	if e != nil {
		t.Fatal(e)
	}
	want, e := jsjson.Stringify(reference)
	if e != nil {
		t.Fatal(e)
	}
	settings, e := s.ParseOrdered("Settings", DefaultSettingsInput())
	if e != nil {
		t.Fatal(e)
	}
	settingsWant, e := jsjson.Stringify(settings)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parsed, e := s.Parse("Settings", DefaultSettingsInput())
			if e != nil {
				t.Error(e)
				return
			}
			parsed["name"] = "mutated"
			parsed["participationWindows"] = []any{"mutated"}
			for _, v := range []struct {
				name string
				raw  []byte
				want string
			}{{"ModelConfig", raw, want}, {"Settings", DefaultSettingsInput(), settingsWant}} {
				out, e := s.ParseOrdered(v.name, v.raw)
				if e != nil {
					t.Error(e)
					return
				}
				text, e := jsjson.Stringify(out)
				if e != nil || text != v.want {
					t.Errorf("shared defaults/order changed: %v", e)
					return
				}
				_ = out.Set("name", "mutated result")
				if providers, ok := out.Get("providers"); ok {
					_ = providers.([]any)[0].(*jsjson.Object).Set("name", "mutated provider")
				}
			}
		}()
	}
	wg.Wait()
	if s.orderedDefinitions != definitions {
		t.Fatal("schema metadata replaced per parse")
	}
	final, e := s.ParseOrdered("ModelConfig", raw)
	if e != nil {
		t.Fatal(e)
	}
	got, e := jsjson.Stringify(final)
	if e != nil || got != want {
		t.Fatal("projection mutation reached schema", e)
	}
	final, e = s.ParseOrdered("Settings", DefaultSettingsInput())
	if e != nil {
		t.Fatal(e)
	}
	got, e = jsjson.Stringify(final)
	if e != nil || got != settingsWant {
		t.Fatal("default mutation reached schema", e)
	}
}
