package pluginmanager

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// BuiltinPolicy is bound by application composition to actual guest operations.
// It also runs while the adapter is disabled. Native persistence only executes
// the ordered credential batch and configuration produced by that guest.
type BuiltinPolicy struct {
	// Configure optionally lends one operation-scoped policy; its close must be idempotent.
	Configure         func() (BuiltinPolicy, func())
	CredentialFailure func(context.Context, error) error
	Validate          func(context.Context, *jsjson.Object) error
	// Prepare transfers ownership of its change list; the host assigns its namespace.
	Prepare func(context.Context, *jsjson.Object, *jsjson.Object) (*jsjson.Object, []credentials.Change, error)
	Secrets func(context.Context) (*jsjson.Object, error)
}
