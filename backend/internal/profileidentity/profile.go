// Package profileidentity owns presentation-only OneBot identity rules. It never
// changes routing IDs, grants authority, or performs network profile lookups.
package profileidentity

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// DisplayName preserves the source's existing 128-UTF-16-unit presentation rule,
// including a split surrogate at that boundary. This is not an ingress limit.
func DisplayName(value any) (string, bool, error) {
	text, ok := value.(string)
	if !ok {
		return "", false, nil
	}
	units, err := jsjson.UTF16(text)
	if err != nil {
		return "", false, err
	}
	out := units[:0]
	for _, u := range units {
		if u <= 0x1f || u == 0x7f || u >= 0x202a && u <= 0x202e || u >= 0x2066 && u <= 0x2069 {
			continue
		}
		out = append(out, u)
	}
	start, end := 0, len(out)
	for start < end && space(out[start]) {
		start++
	}
	for end > start && space(out[end-1]) {
		end--
	}
	out = out[start:end]
	if len(out) > 128 {
		out = out[:128]
	}
	return jsjson.FromUTF16(out), len(out) != 0, nil
}
func space(u uint16) bool {
	return u >= 9 && u <= 13 || u == 0x20 || u == 0xa0 || u == 0x1680 || u >= 0x2000 && u <= 0x200a || u == 0x2028 || u == 0x2029 || u == 0x202f || u == 0x205f || u == 0x3000 || u == 0xfeff
}

var numericQQ = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// QQAvatar accepts a caller-coerced identifier; the domain rule is canonical QQ
// decimal spelling, not a numeric range conversion or arbitrary URL template.
func QQAvatar(id string, group bool) (string, bool) {
	if !numericQQ.MatchString(id) {
		return "", false
	}
	if group {
		return "https://p.qlogo.cn/gh/" + id + "/" + id + "/100", true
	}
	return "https://q1.qlogo.cn/g?b=qq&nk=" + id + "&s=100", true
}

// OneBotID uses exact JS JSON and UTF-8 encoding, including surrogate escaping.
func OneBotID(adapter, account, kind, platform any) (string, error) {
	raw, err := jsjson.Marshal([]any{adapter, account, kind, platform})
	if err != nil {
		return "", err
	}
	return "ob:s:" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// AvatarValue preserves JavaScript RegExp/string coercion for raw legacy JSON
// profile fields. It is not an input-schema validator.
func AvatarValue(value any, group bool) (string, bool, error) {
	id, err := StringValue(value)
	if err != nil {
		return "", false, err
	}
	avatar, ok := QQAvatar(id, group)
	return avatar, ok, nil
}

// StringValue is the JavaScript string coercion used by raw presentation fields.
func StringValue(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case *jsjson.Object:
		if _, ok := v.Get("toString"); ok {
			return "", errors.New("Cannot convert object to primitive value")
		}
		return "[object Object]", nil
	case []any:
		parts := make([]string, len(v))
		for i, x := range v {
			if x != nil {
				s, e := StringValue(x)
				if e != nil {
					return "", e
				}
				parts[i] = s
			}
		}
		return strings.Join(parts, ","), nil
	default:
		return jsjson.Stringify(value)
	}
}
