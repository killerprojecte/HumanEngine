package installation

import (
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/timezones"
	"time"
)

type calendarParts struct{ year, month, day, hour, minute, second, offset int }

func parts(ms int64, zone string) (calendarParts, error) {
	canonical, offset, fixed, ok := timezones.Resolve(zone)
	if !ok {
		return calendarParts{}, errors.New("usage_timezone_invalid")
	}
	if !fixed {
		return namedParts(ms, canonical)
	}
	t := time.UnixMilli(ms + int64(offset)*60000).UTC()
	year := t.Year()
	if year <= 0 {
		year = 1 - year
	}
	return calendarParts{year, int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second(), offset * 60000}, nil
}

// Named zones use pinned ICU, never host timezone data. Preserve repeated and
// half-hour DST segments while producing UTC-instant hour labels.
func usageBucket(parser *jsdate.Parser, at, zone, bucket string) (string, error) {
	ms, valid, err := parser.ParseMillis(at)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", errors.New("usage_date_invalid")
	}
	p, err := parts(ms, zone)
	if err != nil {
		return "", err
	}
	if bucket != "hour" {
		return fmt.Sprintf("%d-%02d-%02d", p.year, p.month, p.day), nil
	}
	milli := ms % 1000
	if milli < 0 {
		milli += 1000
	}
	start := ms - int64(p.minute)*60000 - int64(p.second)*1000 - milli
	same := func(m int) (bool, error) {
		candidate, e := parts(start+int64(m)*60000, zone)
		return candidate.year == p.year && candidate.month == p.month && candidate.day == p.day && candidate.hour == p.hour && candidate.offset == p.offset, e
	}
	first, last := 0, p.minute
	matches, err := same(0)
	if err != nil {
		return "", err
	}
	if !matches {
		for first < last {
			middle := (first + last) / 2
			matches, err = same(middle)
			if err != nil {
				return "", err
			}
			if matches {
				last = middle
			} else {
				first = middle + 1
			}
		}
	}
	t := time.UnixMilli(start + int64(first)*60000).UTC()
	year := t.Year()
	prefix := fmt.Sprintf("%04d", year)
	if year < 0 {
		prefix = fmt.Sprintf("-%06d", -year)
	} else if year > 9999 {
		prefix = fmt.Sprintf("+%06d", year)
	}
	return prefix + t.Format("-01-02T15:04:05.000Z"), nil
}
