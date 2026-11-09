package httpapi

import (
	"net/url"
	"strings"
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
