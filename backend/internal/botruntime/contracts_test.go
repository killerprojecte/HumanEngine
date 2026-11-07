package botruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"os"
	"reflect"
	"testing"
)

func TestRuntimeDefaultsMatchActualSourceSchema(t *testing.T) {
	c := DefaultSettings()
	source := get(contracts, "defaults")
	fields := map[string]any{"persona": c.Persona, "timezone": c.Timezone, "cacheEnabled": c.CacheEnabled, "cacheHours": c.CacheHours, "debounceMs": c.DebounceMS, "maxWaitMs": c.MaxWaitMS, "groupTurnCooldownMs": c.GroupCooldownMS, "directTurnCooldownMs": c.DirectCooldownMS, "sceneTurnConcurrency": float64(c.SceneConcurrency), "topicHistoryMaxMessages": float64(c.TopicHistoryMaxMessages), "promptContextWindowEnabled": c.PromptContextWindowEnabled, "promptContextWindowMaxMessages": float64(c.Window.MaxMessages), "promptContextWindowRetainMessages": float64(c.Window.RetainMessages), "promptContextWindowMaxBytes": float64(c.Window.MaxBytes), "replySuggestionsEnabled": c.ReplySuggestionsEnabled, "topicAdmissionEnabled": c.TopicAdmissionEnabled, "replyEvidenceMaxBytes": c.ReplyEvidenceMaxBytes, "plannerEvidenceMaxBytes": c.PlannerEvidenceMaxBytes}
	for key, actual := range fields {
		if !reflect.DeepEqual(actual, get(source, key)) {
			t.Errorf("default %s = %#v, source %#v", key, actual, get(source, key))
		}
	}
	if len(c.ParticipationWindows) != len(array(get(source, "participationWindows"))) {
		t.Fatal("participation windows differ")
	}
}
func TestRuntimeSourceFingerprintsMatch(t *testing.T) {
	data, e := os.ReadFile("testdata/runtime.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct{ Source map[string]string }
	if e = json.Unmarshal(data, &fixture); e != nil {
		t.Fatal(e)
	}
	for path, expected := range fixture.Source {
		data, e := os.ReadFile("../../../" + path)
		if e != nil {
			t.Fatal(e)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != expected {
			t.Errorf("source changed: %s", path)
		}
	}
}
func TestRuntimeOwnsPublishedConfigurationWindows(t *testing.T) {
	r, _, _ := testRuntime(t, ModelFunc(func(_ context.Context, _ ModelRequest, done func(Completion, error)) {
		done(fixtureDecision("silent"), nil)
	}))
	cfg := r.config()
	cfg.ParticipationWindows = []ParticipationWindow{{0, 24, "quiet"}}
	if err := r.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ParticipationWindows[0].Inclination = "engaged"
	if r.config().ParticipationWindows[0].Inclination != "quiet" {
		t.Fatal("Configure retained caller slice")
	}
	copy := cfg
	second, err := New(copy, r.services)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	copy.ParticipationWindows[0].Inclination = "balanced"
	if second.config().ParticipationWindows[0].Inclination != "engaged" {
		t.Fatal("New retained caller slice")
	}
}
func TestToolInputsMatchActualSourceSchemas(t *testing.T) {
	for _, path := range []string{"testdata/schema.json", "testdata/undefined-wire.json"} {
		t.Run(path, func(t *testing.T) { testToolSchemaFile(t, path) })
	}
}
func testToolSchemaFile(t *testing.T, path string) {
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Kind, Name, Input string
		Expected          *string
	}
	if e = json.Unmarshal(data, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Kind+"/"+c.Name, func(t *testing.T) {
			input, e := jsjson.Parse(c.Input)
			if e != nil {
				t.Fatal(e)
			}
			schema := "decisionSchema"
			if c.Kind == "send" {
				schema = "sendSchema"
			}
			value, e := schemaValue(get(contracts, schema), input)
			if c.Expected == nil {
				if e == nil {
					t.Fatal("source rejected input accepted natively")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			actual, e := jsjson.Stringify(value)
			if e != nil {
				t.Fatal(e)
			}
			if actual != *c.Expected {
				t.Fatalf("got %s, source %s", actual, *c.Expected)
			}
		})
	}
}

func TestCompiledToolSchemaKeywords(t *testing.T) {
	assertContractSchema(get(contracts, "decisionSchema").(*Object))
	assertContractSchema(get(contracts, "sendSchema").(*Object))
}

func assertContractSchema(schema *Object) {
	for _, m := range schema.Members() {
		switch m.Key {
		case "$schema", "description", "type", "const", "enum", "required", "default", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems":
		case "properties":
			for _, property := range m.Value.(*Object).Members() {
				assertContractSchema(property.Value.(*Object))
			}
		case "items":
			assertContractSchema(m.Value.(*Object))
		case "additionalProperties":
			if value, ok := m.Value.(*Object); ok {
				assertContractSchema(value)
			}
		case "anyOf", "oneOf":
			for _, value := range array(m.Value) {
				assertContractSchema(value.(*Object))
			}
		default:
			panic("unsupported_runtime_schema_keyword:" + m.Key)
		}
	}
}
