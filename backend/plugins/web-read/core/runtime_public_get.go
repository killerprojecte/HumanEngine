//go:build web_read_public_get && !web_read_candidate

package webread

import "encoding/json"

func runtimeInvoke(input []byte, operation string) any {
	switch operation {
	case "publicGetRegistrations":
		var definitions []json.RawMessage
		if err := json.Unmarshal(registrations, &definitions); err != nil || len(definitions) == 0 {
			return map[string]any{"error": "invalid_plugin_registrations"}
		}
		// Only webpage GET, never the structured request and planned send.
		return definitions[:1]
	case "publicGetResponse":
		var observations map[string]any
		if err := json.Unmarshal(input, &observations); err != nil {
			return map[string]any{"error": "invalid_plugin_input"}
		}
		observations["tool"] = "web-read.read"
		response := ResponseResult(observations).(map[string]any)
		// Keep the readable preview once. The complete original remains in
		// the turn-owned cache, not repeated in the Planner context.
		if reference := obj(response["body"]); reference != nil {
			delete(reference, "content")
		}
		delete(response, "headers")
		delete(response, "rawHeaders")
		return response
	}
	return map[string]any{"error": "unknown_plugin_operation"}
}
