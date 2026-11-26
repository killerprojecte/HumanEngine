//go:build bundled_wasm

package main

import (
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func previewBuiltins() ([]string, error) {
	// Verify shipped modules without enabling any extra builtin or owner.
	for _, id := range plugins.BundledArtifactIDs() {
		if artifact, err := plugins.BuiltinArtifact(id); err != nil || len(artifact) == 0 {
			return nil, errors.New("runtime_preview_builtin_artifact_required")
		}
	}
	return nil, nil // Keep the packaged distribution's builtin defaults.
}
