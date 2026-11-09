//go:build bundled_wasm

package typingdelay

import "embed"

//go:embed dist/typing-delay.wasm
var artifact embed.FS

func Artifact() ([]byte, error) { return artifact.ReadFile("dist/typing-delay.wasm") }
