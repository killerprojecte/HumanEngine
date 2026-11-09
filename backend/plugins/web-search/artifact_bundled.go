//go:build bundled_wasm

package websearch

import "embed"

//go:embed dist/web-search.wasm
var artifact embed.FS

func Artifact() ([]byte, error) { return artifact.ReadFile("dist/web-search.wasm") }
