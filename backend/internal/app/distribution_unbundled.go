//go:build !bundled_wasm || without_snowluma

package app

import (
	"context"
	"errors"
)

func (*Application) bindDistributionBuiltin(context.Context, Owners, string) error {
	return errors.New("builtin_runtime_not_implemented")
}
