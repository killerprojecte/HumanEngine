package contract

import "github.com/killerprojecte/HumanEngine/backend/internal/timezones"

func validTimeZone(value string) bool {
	_, _, _, ok := timezones.Resolve(value)
	return ok
}
