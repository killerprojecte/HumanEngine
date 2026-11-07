//go:build bundled_wasm

package plugins

import "embed"

//go:embed builtins/*.wasm
var bundled embed.FS

func BuiltinArtifact(id string) ([]byte, error) { return bundled.ReadFile("builtins/" + id + ".wasm") }
