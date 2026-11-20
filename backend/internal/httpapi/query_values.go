package httpapi

import (
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsstring"
)

// honoQueryValues mirrors Hono req.queries, not net/url.ParseQuery or WHATWG
// URLSearchParams. Semicolons are ordinary data. Malformed percent/UTF-8 groups
// are retained by Hono's forgiving decodeURIComponent helper, not dropped.
func honoQueryValues(raw string) url.Values {
	raw, _, _ = strings.Cut(raw, "#")
	values := url.Values{}
	decode := func(s string) string { return habitTryDecode(strings.ReplaceAll(s, "+", " "), false) }
	for _, pair := range strings.Split(raw, "&") {
		key, value, _ := strings.Cut(pair, "=")
		key = decode(key)
		if key == "" {
			continue
		}
		values[key] = append(values[key], decode(value))
	}
	return values
}

func coerceQueryInteger(text string, maximum float64) (int64, bool) {
	text = jsstring.Trim(text)
	var n float64
	var err error
	if len(text) > 2 && text[0] == '0' && strings.ContainsAny(text[1:2], "xXoObB") {
		if strings.ContainsRune(text, '_') {
			return 0, false
		}
		var u uint64
		u, err = strconv.ParseUint(text, 0, 64)
		n = float64(u)
	} else {
		if !experienceDecimal.MatchString(text) {
			return 0, false
		}
		n, err = strconv.ParseFloat(text, 64)
	}
	if err != nil || n < 1 || n > maximum || math.Trunc(n) != n {
		return 0, false
	}
	return int64(n), true
}
