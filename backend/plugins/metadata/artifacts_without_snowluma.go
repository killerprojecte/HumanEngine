//go:build bundled_wasm && without_snowluma

package metadata

const snowLumaBundled = false

func optionalArtifact(id string) ([]byte, error) { return missingArtifact(id) }
