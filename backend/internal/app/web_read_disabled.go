//go:build !web_read_candidate

package app

import (
	"context"
	"errors"
)

// HTTP 101 is still pending. Explicitly requesting this builtin cannot admit a
// candidate through the default application, even if a candidate dist is present.
func (a *Application) bindWebRead(context.Context) error {
	return errors.New("builtin_runtime_not_implemented")
}
