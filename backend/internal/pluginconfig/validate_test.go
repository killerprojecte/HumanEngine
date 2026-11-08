package pluginconfig

import (
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/plugins/metadata"
	"testing"
)

func TestBuiltinManifestVocabulary(t *testing.T) {
	for _, id := range []string{"snowluma", "typing-delay", "web-read", "web-search"} {
		t.Run(id, func(t *testing.T) {
			raw := metadata.Manifest(id).(json.RawMessage)
			manifest := parse(t, string(raw)).(*jsjson.Object)
			schema := object(field(manifest, "configSchema"))
			if schema == nil {
				t.Fatal("schema missing")
			}
			input := new(jsjson.Object)
			if id == "snowluma" {
				_ = input.Set("url", "ws://localhost")
				_ = input.Set("accountId", "123")
			}
			if err := ValidateBuiltin(input, schema); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestUTF16AndIdentityEnum(t *testing.T) {
	for _, tc := range []struct{ schema, value, want string }{
		{`{"type":"string","maxLength":1}`, `"😀"`, "expected_string"},
		{`{"type":"string","maxLength":1}`, `"\ud800"`, ""},
		{`{"type":"object","enum":[{}]}`, `{}`, "invalid_enum"},
		{`{"type":"object","properties":{}}`, `{"unknown":1}`, "unknown_property"},
		{`{"type":"string","pattern":"(?<=a)b"}`, `"ab"`, "unsupported_builtin_schema_pattern"},
		{`{"type":"string","pattern":"^[A-Z][A-Z0-9_]*$"}`, `"ABC\n"`, "expected_string"},
		{`{"type":"string","pattern":"^\\/[a-zA-Z0-9/_-]+$"}`, `"/abc_/-"`, ""},
	} {
		err := ValidateBuiltin(parse(t, tc.value), parse(t, tc.schema).(*jsjson.Object))
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Fatalf("%s %s got %s want %s", tc.schema, tc.value, got, tc.want)
		}
	}
}
