// Package websearch owns the web-search plugin's resources.
package websearch

import (
	_ "embed"
	"encoding/json"
)

//go:embed manifest.json
var manifest []byte

// Manifest retains the original schema property order and returns an owned copy.
func Manifest() any {
	if !json.Valid(manifest) {
		return map[string]any{"error": "builtin_manifest_invalid"}
	}
	return json.RawMessage(append([]byte(nil), manifest...))
}
