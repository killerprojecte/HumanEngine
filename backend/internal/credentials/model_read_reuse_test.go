package credentials

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sync"
	"testing"
)

func TestModelReadReusesSchemaButNotPresenceOrValues(t *testing.T) {
	schema, e := modelReadSchemas()
	if e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{"providers":[{"id":"p","name":"Synthetic","protocol":"openai-chat","baseUrl":"https://synthetic.invalid"}],"models":[],"routes":[]}`)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, e := modelReadSchemas()
			if e != nil || got != schema {
				t.Errorf("compiled owner not reused: %p %p %v", got, schema, e)
				return
			}
			calls := 0
			want := i%2 == 0
			value, e := ReadModelConfiguration(raw, func(namespace, name string) (bool, error) {
				calls++
				if namespace != "model" || name != "p" {
					t.Errorf("wrong key")
				}
				return want, nil
			})
			if e != nil {
				t.Error(e)
				return
			}
			provider := modelField(value, "providers").([]any)[0].(*jsjson.Object)
			if modelField(provider, "credentialConfigured") != want || calls != 1 {
				t.Errorf("presence cached or lost")
			}
			_ = provider.Set("name", "mutated result")
		}(i)
	}
	wg.Wait()
	value, e := ReadModelConfiguration(raw, func(string, string) (bool, error) { return false, nil })
	if e != nil {
		t.Fatal(e)
	}
	provider := modelField(value, "providers").([]any)[0].(*jsjson.Object)
	if modelField(provider, "name") != "Synthetic" || modelField(provider, "credentialConfigured") != false {
		t.Fatal("mutable projection leaked")
	}
}
