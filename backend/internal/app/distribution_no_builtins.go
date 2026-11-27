//go:build !bundled_wasm

package app

import "errors"

func distributionBuiltins() []string { return nil }

func (*Application) bindTypingDelay() error { return errors.New("builtin_wasm_not_bundled") }
