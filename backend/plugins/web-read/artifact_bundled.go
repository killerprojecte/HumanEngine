//go:build bundled_wasm

package webread

import "embed"

//go:embed dist/web-read.wasm
var artifact embed.FS

func Artifact() ([]byte, error) { return artifact.ReadFile("dist/web-read.wasm") }
