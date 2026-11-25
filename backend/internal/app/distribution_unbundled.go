//go:build !bundled_wasm

package app

import (
	"context"
	"errors"
)

func distributionBuiltins() []string { return nil }

func (*Application) bindDistributionBuiltin(context.Context, Owners, string) error {
	return errors.New("builtin_runtime_not_implemented")
}
