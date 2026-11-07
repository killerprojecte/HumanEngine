//go:build !bundled_wasm

package plugins

import "errors"

func BuiltinArtifact(string) ([]byte, error) {
	return nil, errors.New("builtin_wasm_not_bundled: build with make bundle")
}
