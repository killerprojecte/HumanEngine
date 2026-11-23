//go:build !bundled_wasm

package main

func previewBuiltins() ([]string, error) {
	return []string{}, nil // Empty, not the application default.
}
