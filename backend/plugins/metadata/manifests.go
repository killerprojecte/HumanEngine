// Package metadata preserves builtin identifiers and management form schemas.
package metadata

import (
	"embed"
	"encoding/json"
)

//go:embed *.json
var manifests embed.FS

func Manifest(id string) any {
	data, err := manifests.ReadFile(id + ".json")
	if err != nil {
		return map[string]any{"error": "builtin_manifest_missing"}
	}
	if !json.Valid(data) {
		return map[string]any{"error": "builtin_manifest_invalid"}
	}
	// Keep original property order: the existing frontend uses schema order for forms.
	return json.RawMessage(data)
}
