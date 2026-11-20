//go:build web_read_candidate && cgo

package webreadruntime

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/pinnedhop"
	host "github.com/killerprojecte/HumanEngine/backend/plugins/web-read/host"
)

// The per-lookup work lease stays held until the cancellation-aware child is
// reaped. Installation does not start an idle process.
func (*installation) openResolver() (host.Lookup, error) {
	return pinnedhop.LookupChild, nil
}
