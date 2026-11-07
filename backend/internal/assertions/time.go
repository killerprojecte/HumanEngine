package assertions

import (
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"time"
)

var utcDate, _ = jsdate.New("UTC")

func isoMillis(ms int64) string {
	t := time.UnixMilli(ms).UTC()
	y := t.Year()
	year := fmt.Sprintf("%04d", y)
	if y < 0 {
		year = fmt.Sprintf("-%06d", -y)
	} else if y > 9999 {
		year = fmt.Sprintf("+%06d", y)
	}
	return fmt.Sprintf("%s-%02d-%02dT%02d:%02d:%02d.%03dZ", year, t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond()/1000000)
}
func validTime(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	ms, valid, e := utcDate.ParseMillis(s)
	return e == nil && valid && isoMillis(ms) == s
}
