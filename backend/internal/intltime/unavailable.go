//go:build !humanengine_icu || !cgo

package intltime

import "github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"

func namedHour(_ int64, _ string) (int, error) { return 0, nodeicu.ErrUnavailable }
