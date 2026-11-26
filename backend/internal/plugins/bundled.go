//go:build bundled_wasm

package plugins

import "github.com/killerprojecte/HumanEngine/backend/plugins/metadata"

func BuiltinArtifact(id string) ([]byte, error) { return metadata.Artifact(id) }

func BundledArtifactIDs() []string { return metadata.ArtifactIDs() }
