package modelcalls

import (
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// ParseQuery follows the source coerce.number fields. Unknown keys are stripped;
// repeated values are arrays in Hono and therefore do not pass these fields.
func ParseQuery(values url.Values) (Query, error) {
	q := Query{Limit: 50}
	single := func(key string) (*string, error) {
		v, ok := values[key]
		if !ok {
			return nil, nil
		}
		if len(v) != 1 {
			return nil, errors.New("invalid_model_calls_query")
		}
		return &v[0], nil
	}
	for _, key := range []string{"limit", "beforeId"} {
		value, err := single(key)
		if err != nil {
			return q, err
		}
		if value == nil {
			continue
		}
		n, err := coerceQueryNumber(*value)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 1 || n > 9007199254740991 {
			return q, errors.New("invalid_model_calls_query")
		}
		if key == "limit" {
			if n > 100 {
				return q, errors.New("invalid_model_calls_limit")
			}
			q.Limit = int(n)
		} else {
			v := int64(n)
			q.BeforeID = &v
		}
	}
	var err error
	if q.Role, err = single("role"); err != nil {
		return q, err
	}
	if q.ModelID, err = single("modelId"); err != nil {
		return q, err
	}
	if q.Status, err = single("status"); err != nil {
		return q, err
	}
	return q, q.Validate()
}
func coerceQueryNumber(value string) (float64, error) {
	s := strings.TrimFunc(value, func(r rune) bool {
		return strings.ContainsRune("\u0009\u000a\u000b\u000c\u000d\u0020\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff", r)
	})
	if s == "" {
		return 0, nil
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			n, err := strconv.ParseUint(s[2:], base, 64)
			return float64(n), err
		}
	}
	// Go additionally accepts hexadecimal floats and underscores; JavaScript's
	// decimal Number conversion does not.
	if strings.ContainsAny(s, "_pPxX") {
		return 0, errors.New("invalid_number")
	}
	return strconv.ParseFloat(s, 64)
}
