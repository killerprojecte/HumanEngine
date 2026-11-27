//go:build cgo

package publicpage

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
)

// NativeLookup runs the pinned installation's cancellable resolver child.
func NativeLookup(ctx context.Context, hostname string) pinnedhop.LookupJob {
	return pinnedhop.LookupChild(ctx, hostname)
}
