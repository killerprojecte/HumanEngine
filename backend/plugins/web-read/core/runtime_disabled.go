//go:build !web_read_candidate

package webread

// Normal/distributed builds retain manifest/extract only. Experimental runtime
// operations cannot accidentally become a shipped web-read service.
func runtimeInvoke([]byte, string) any {
	return map[string]any{"error": "unknown_plugin_operation"}
}
