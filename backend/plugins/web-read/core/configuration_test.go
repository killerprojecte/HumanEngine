//go:build web_read_candidate

package webread

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestConfigurationAndRegistrationsMatchActualSource(t *testing.T) {
	path := os.Getenv("WEB_READ_INSTALLATION_FIXTURE")
	if path == "" {
		t.Fatal("WEB_READ_INSTALLATION_FIXTURE must name freshly generated actual source fixture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases       []json.RawMessage `json:"cases"`
		ToolCases   []json.RawMessage `json:"toolCases"`
		Definitions any               `json:"definitions"`
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures.Cases) != 23 {
		t.Fatal("missing source cases", len(fixtures.Cases))
	}
	if len(fixtures.ToolCases) != 30 {
		t.Fatal("missing manager source cases", len(fixtures.ToolCases))
	}
	for _, input := range append(fixtures.Cases, fixtures.ToolCases...) {
		var testcase struct {
			Expected any `json:"expected"`
		}
		if err = json.Unmarshal(input, &testcase); err != nil {
			t.Fatal(err)
		}
		output, _ := json.Marshal(Invoke(input))
		var actual any
		_ = json.Unmarshal(output, &actual)
		if !reflect.DeepEqual(testcase.Expected, actual) {
			t.Fatalf("source mismatch %s: %s", input, output)
		}
	}
	raw, _ = json.Marshal(Invoke([]byte(`{"operation":"registrations"}`)))
	var actual any
	_ = json.Unmarshal(raw, &actual)
	if !reflect.DeepEqual(fixtures.Definitions, actual) {
		t.Fatal("source registration mismatch")
	}
}
