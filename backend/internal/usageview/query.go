// Package usageview projects the source accounting tables without running model work.
package usageview

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/timezones"
)

type Range struct{ Start, End, Bucket, Zone string }

var instant = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]+)?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func canonicalInstant(parser *jsdate.Parser, s string) (string, int64, error) {
	invalid := errors.New("usage_invalid_instant")
	m := instant.FindStringSubmatch(s)
	if m == nil {
		return "", 0, invalid
	}
	y, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	date := time.Date(y, time.Month(month), d, 0, 0, 0, 0, time.UTC)
	if date.Year() != y || int(date.Month()) != month || date.Day() != d {
		return "", 0, invalid
	}
	ms, valid, e := parser.ParseMillis(s)
	if e != nil {
		return "", 0, e
	}
	if !valid {
		return "", 0, invalid
	}
	t := time.UnixMilli(ms).UTC()
	prefix := fmt.Sprintf("%04d", t.Year())
	if t.Year() < 0 {
		prefix = fmt.Sprintf("-%06d", -t.Year())
	} else if t.Year() > 9999 {
		prefix = fmt.Sprintf("+%06d", t.Year())
	}
	return prefix + t.Format("-01-02T15:04:05.000Z"), ms, nil
}

// ParseQuery preserves Hono's repeated-value rejection and strips unknown keys.
func ParseQuery(values url.Values, parser *jsdate.Parser) (*Range, error) {
	fields := map[string]string{}
	present := false
	for _, k := range []string{"start", "end", "bucket", "timeZone"} {
		v, ok := values[k]
		if !ok {
			continue
		}
		present = true
		if len(v) != 1 {
			return nil, errors.New("usage_repeated_query")
		}
		fields[k] = v[0]
	}
	if !present {
		return nil, nil
	}
	if fields["start"] == "" || fields["end"] == "" {
		return nil, errors.New("usage_range_required")
	}
	q := &Range{Bucket: "day"}
	if b, ok := fields["bucket"]; ok {
		if b != "hour" && b != "day" {
			return nil, errors.New("usage_invalid_bucket")
		}
		q.Bucket = b
	}
	if z, ok := fields["timeZone"]; ok {
		n, e := jsjson.UTF16Len(z)
		if e != nil || n == 0 || n > 100 {
			return nil, errors.New("usage_invalid_timezone")
		}
		canonical, _, _, ok := timezones.Resolve(z)
		if !ok {
			return nil, errors.New("usage_invalid_timezone")
		}
		q.Zone = canonical
	}
	var start, end int64
	var e error
	q.Start, start, e = canonicalInstant(parser, fields["start"])
	if e != nil {
		return nil, e
	}
	q.End, end, e = canonicalInstant(parser, fields["end"])
	if e != nil {
		return nil, e
	}
	if start >= end {
		return nil, errors.New("usage_unordered_range")
	}
	return q, nil
}
