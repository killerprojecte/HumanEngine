//go:build bundled_wasm && without_snowluma

package app

func distributionBuiltins() []string { return []string{"typing-delay"} }
