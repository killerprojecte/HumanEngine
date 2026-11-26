//go:build bundled_wasm

package metadata

import (
	"io/fs"

	typingdelay "github.com/killerprojecte/HumanEngine/backend/plugins/typing-delay"
	webread "github.com/killerprojecte/HumanEngine/backend/plugins/web-read"
	websearch "github.com/killerprojecte/HumanEngine/backend/plugins/web-search"
)

// ArtifactIDs lists the modules embedded by this distribution, not the ones it enables.
func ArtifactIDs() []string {
	ids := []string{"typing-delay", "web-read", "web-search"}
	if snowLumaBundled {
		ids = append(ids, "snowluma")
	}
	return ids
}

func Artifact(id string) ([]byte, error) {
	switch id {
	case "typing-delay":
		return typingdelay.Artifact()
	case "web-read":
		return webread.Artifact()
	case "web-search":
		return websearch.Artifact()
	default:
		return optionalArtifact(id)
	}
}

func missingArtifact(id string) ([]byte, error) {
	return nil, &fs.PathError{Op: "open", Path: "builtins/" + id + ".wasm", Err: fs.ErrNotExist}
}
