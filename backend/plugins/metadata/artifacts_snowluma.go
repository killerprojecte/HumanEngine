//go:build bundled_wasm && !without_snowluma

package metadata

import snowluma "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma"

const snowLumaBundled = true

func optionalArtifact(id string) ([]byte, error) {
	if id == "snowluma" {
		return snowluma.Artifact()
	}
	return missingArtifact(id)
}
