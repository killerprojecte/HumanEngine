//go:build web_read_candidate

package webread

import "encoding/json"

// configuration is the source WebReadConfigSchema contract. Native composition
// transports its result; it does not parse plugin policy or manufacture defaults.
func configuration(input []byte) any {
	var in struct {
		Config any `json:"config"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return map[string]any{"error": "invalid_plugin_input"}
	}
	config, ok := in.Config.(map[string]any)
	if !ok || !keys(config, "timeoutMs", "maxResponseBytes", "maxChars", "maxConcurrency") {
		return map[string]any{"valid": false}
	}
	defaults := map[string]any{"timeoutMs": float64(60000), "maxChars": float64(4000), "maxConcurrency": float64(2)}
	for key, value := range config {
		min, max := float64(0), float64(9007199254740991)
		switch key {
		case "timeoutMs":
			min, max = 500, 2147483647
		case "maxChars":
			min, max = 256, 30000
		case "maxConcurrency":
			min, max = 1, 64
		}
		if !integer(value, min, max) {
			return map[string]any{"valid": false}
		}
		defaults[key] = value
	}
	return map[string]any{"valid": true, "config": defaults}
}
