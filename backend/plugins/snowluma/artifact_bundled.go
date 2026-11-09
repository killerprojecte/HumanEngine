//go:build bundled_wasm

package snowluma

import "embed"

//go:embed dist/snowluma.wasm
var artifact embed.FS

func Artifact() ([]byte, error) { return artifact.ReadFile("dist/snowluma.wasm") }
