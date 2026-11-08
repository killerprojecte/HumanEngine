package contract

import (
	"sync"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestSharedSchemasIdentityAndIndependentResults(t *testing.T) {
	shared, err := SharedSchemas()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := NewSchemas()
	if err != nil {
		t.Fatal(err)
	}
	if fresh == shared {
		t.Fatal("fresh constructor became shared")
	}
	expected, err := shared.ParseOrdered("Settings", DefaultSettingsInput())
	if err != nil {
		t.Fatal(err)
	}
	want, err := jsjson.Stringify(expected)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := SharedSchemas()
			if err != nil || got != shared {
				t.Errorf("shared identity changed: %v", err)
				return
			}
			parsed, err := got.ParseOrdered("Settings", DefaultSettingsInput())
			if err != nil {
				t.Error(err)
				return
			}
			encoded, err := jsjson.Stringify(parsed)
			if err != nil || encoded != want {
				t.Errorf("default mutation leaked: %v", err)
				return
			}
			if parsed == expected {
				t.Error("result identity shared")
			}
			_ = parsed.Set("name", "mutated caller result")
			plain, err := got.Parse("Settings", DefaultSettingsInput())
			if err != nil {
				t.Error(err)
				return
			}
			plain["participationWindows"] = []any{"mutated"}
			if _, err = got.ParseOrdered("Settings", []byte(`{"name":false}`)); err == nil {
				t.Error("validation result reused for new invalid input")
			}
		}()
	}
	wg.Wait()
	again, err := shared.ParseOrdered("Settings", DefaultSettingsInput())
	if err != nil {
		t.Fatal(err)
	}
	actual, err := jsjson.Stringify(again)
	if err != nil || actual != want {
		t.Fatal("callers changed shared defaults", err)
	}
}
