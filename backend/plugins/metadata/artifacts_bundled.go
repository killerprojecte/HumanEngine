//go:build bundled_wasm

package metadata

import (
	snowluma "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma"
	typingdelay "github.com/killerprojecte/HumanEngine/backend/plugins/typing-delay"
	webread "github.com/killerprojecte/HumanEngine/backend/plugins/web-read"
	websearch "github.com/killerprojecte/HumanEngine/backend/plugins/web-search"
	"io/fs"
)

func Artifact(id string) ([]byte, error) {
	switch id {
	case "typing-delay":
		return typingdelay.Artifact()
	case "web-read":
		return webread.Artifact()
	case "web-search":
		return websearch.Artifact()
	case "snowluma":
		return snowluma.Artifact()
	default:
		return nil, &fs.PathError{Op: "open", Path: "builtins/" + id + ".wasm", Err: fs.ErrNotExist}
	}
}
