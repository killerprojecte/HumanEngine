package providertransport

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
)

// WithPhysicalWork binds borrowed model operations to a caller-owned physical
// lifetime, including role-timeout policy runners and synchronous metadata SQL.
// The caller holds a root until logical work ends, then waits without closing
// the shared model or policy owner.
func WithPhysicalWork(ctx context.Context, work *quiescence.Group) context.Context {
	return quiescence.WithGroup(ctx, work)
}
func physicalChild(ctx context.Context) (func(), bool) { return quiescence.ContextChild(ctx) }
