//go:build !humanengine_icu || !cgo

package installation

import "github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"

func namedParts(ms int64, zone string) (calendarParts, error) {
	return calendarParts{}, nodeicu.ErrUnavailable
}
