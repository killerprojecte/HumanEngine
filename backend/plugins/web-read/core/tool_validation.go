//go:build web_read_candidate

package webread

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginconfig"
)

// toolValidation runs the source manager's validateConfig before service.read /
// service.request. This deliberately preserves unsupported_schema_keyword for
// the source headers schema's propertyNames rather than silently widening it.
// The shared schema vocabulary is compiled into this guest, not a native policy.
func toolValidation(input []byte) any {
	value, err := jsjson.ParseBytes(input)
	if err != nil {
		return map[string]any{"error": "invalid_plugin_input"}
	}
	in, ok := value.(*jsjson.Object)
	if !ok {
		return map[string]any{"error": "invalid_plugin_input"}
	}
	name, _ := in.Get("tool")
	data, _ := in.Get("input")
	value, err = jsjson.ParseBytes(registrations)
	if err != nil {
		return map[string]any{"error": "invalid_plugin_registration"}
	}
	for _, value := range value.([]any) {
		tool := value.(*jsjson.Object)
		id, _ := tool.Get("name")
		if id != name {
			continue
		}
		schema, _ := tool.Get("inputSchema")
		if err = pluginconfig.ValidateBuiltin(data, schema.(*jsjson.Object)); err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"valid": true}
	}
	return map[string]any{"error": "tool_missing"}
}
