// Package intltime preserves the Runtime participation-window hour calculation:
// Intl.DateTimeFormat('en', {hour:'numeric', hourCycle:'h23', timeZone}).
package intltime

import (
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/timezones"
)

// Hour returns the source h23 hour for an integral ECMAScript Date timestamp.
// It never uses the host timezone database or starts a Node process.
func Hour(milliseconds int64, timezone string) (int, error) {
	if milliseconds < -8640000000000000 || milliseconds > 8640000000000000 {
		return 0, fmt.Errorf("intltime: timestamp outside ECMAScript Date range")
	}
	zone, offset, fixed, ok := timezones.Resolve(timezone)
	if !ok {
		return 0, fmt.Errorf("intltime: invalid timezone %q", timezone)
	}
	if fixed {
		ms := (milliseconds + int64(offset)*60000) % 86400000
		if ms < 0 {
			ms += 86400000
		}
		return int(ms / 3600000), nil
	}
	return namedHour(milliseconds, zone)
}
