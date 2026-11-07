//go:build !humanengine_icu || !cgo

package jsdate

import (
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
)

// New supports explicit UTC without ICU and refuses implicit host timezone fallback.
func New(sourceResolvedTimezone string) (*Parser, error) {
	if sourceResolvedTimezone == "UTC" {
		return &utcParser, nil
	}
	return nil, fmt.Errorf("jsdate: timezone %q requires pinned native ICU: %w", sourceResolvedTimezone, nodeicu.ErrUnavailable)
}
