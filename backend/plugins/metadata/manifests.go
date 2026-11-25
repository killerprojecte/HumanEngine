// Package metadata aggregates resources owned by individual builtin plugins.
package metadata

import (
	typingdelay "github.com/killerprojecte/HumanEngine/backend/plugins/typing-delay"
	webread "github.com/killerprojecte/HumanEngine/backend/plugins/web-read"
	websearch "github.com/killerprojecte/HumanEngine/backend/plugins/web-search"
)

func Manifest(id string) any {
	switch id {
	case "typing-delay":
		return typingdelay.Manifest()
	case "web-read":
		return webread.Manifest()
	case "web-search":
		return websearch.Manifest()
	default:
		return map[string]any{"error": "builtin_manifest_missing"}
	}
}
