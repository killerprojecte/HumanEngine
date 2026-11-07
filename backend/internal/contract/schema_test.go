package contract

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestSettingsDefaultsMatchNode(t *testing.T) {
	b, e := os.ReadFile("testdata/settings-node.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Input  json.RawMessage
		Output map[string]any
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	s, e := NewSchemas()
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Parse("Settings", f.Input)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got, f.Output) {
		t.Fatal("default projection differs")
	}
	for _, mutation := range []map[string]any{{"unexpected": true}, {"name": nil}, {"timezone": "not/a/timezone"}, {"budgetDailyUsd": 0.01}, {"promptContextWindowRetainMessages": 80}} {
		var input map[string]any
		_ = json.Unmarshal(f.Input, &input)
		for k, v := range mutation {
			input[k] = v
		}
		b, _ := json.Marshal(input)
		if _, e = s.Parse("Settings", b); e == nil {
			t.Fatalf("accepted invalid input: %v", mutation)
		}
	}
}

func TestModelProviderURLMatchesNode(t *testing.T) {
	b, err := os.ReadFile("testdata/provider-urls-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Input  string
		Valid  bool
		Output string
	}
	if err = json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	schemas, err := NewSchemas()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		provider := map[string]any{"id": "fixture", "name": "fixture", "protocol": "openai-chat", "baseUrl": fixture.Input}
		input, _ := json.Marshal(map[string]any{"providers": []any{provider}, "models": []any{}, "routes": []any{}})
		got, err := schemas.Parse("ModelConfig", input)
		if (err == nil) != fixture.Valid {
			t.Errorf("URL fixture %q: accepted=%t want=%t", fixture.Input, err == nil, fixture.Valid)
		}
		if fixture.Valid && err == nil && got["providers"].([]any)[0].(map[string]any)["baseUrl"] != fixture.Output {
			t.Errorf("URL fixture %q returned different normalized value", fixture.Input)
		}
	}
}

func TestModelDefaultsAndRefinementsMatchNode(t *testing.T) {
	bytes, err := os.ReadFile("testdata/models-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Input  json.RawMessage
		Valid  bool
		Output map[string]any
	}
	if err = json.Unmarshal(bytes, &fixtures); err != nil {
		t.Fatal(err)
	}
	schemas, err := NewSchemas()
	if err != nil {
		t.Fatal(err)
	}
	for i, fixture := range fixtures {
		got, err := schemas.Parse("ModelConfig", fixture.Input)
		if (err == nil) != fixture.Valid {
			t.Errorf("fixture %d acceptance differs: %v", i, err)
			continue
		}
		if fixture.Valid && !reflect.DeepEqual(got, fixture.Output) {
			t.Errorf("fixture %d defaults differ", i)
		}
	}
}

func TestTimeZoneAcceptanceMatchesNode(t *testing.T) {
	bytes, err := os.ReadFile("testdata/timezones-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Input string
		Valid bool
	}
	if err = json.Unmarshal(bytes, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		if validTimeZone(fixture.Input) != fixture.Valid {
			t.Errorf("timezone %q acceptance differs", fixture.Input)
		}
	}
}

func TestSceneRevisionBoundsMatchNode(t *testing.T) {
	bytes, err := os.ReadFile("testdata/scenes-node.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Input  json.RawMessage
		Valid  bool
		Output map[string]any
	}
	if err = json.Unmarshal(bytes, &fixtures); err != nil {
		t.Fatal(err)
	}
	schemas, err := NewSchemas()
	if err != nil {
		t.Fatal(err)
	}
	for i, fixture := range fixtures {
		got, err := schemas.Parse("Scene", fixture.Input)
		if (err == nil) != fixture.Valid {
			t.Errorf("scene %d acceptance differs", i)
		}
		if fixture.Valid && !reflect.DeepEqual(got, fixture.Output) {
			t.Errorf("scene %d output differs", i)
		}
	}
}
