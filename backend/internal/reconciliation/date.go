package reconciliation

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"strings"
)

// Date.parse first applies ToString. Persisted validUntil is normally a string,
// but legacy JSON null/number/array values retain JavaScript coercion semantics.
func jsDateString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case nil:
		return "null", nil
	case *Object:
		return "[object Object]", nil
	case []any:
		parts := make([]string, len(x))
		for i, v := range x {
			if v != nil {
				s, e := jsDateString(v)
				if e != nil {
					return "", e
				}
				parts[i] = s
			}
		}
		return strings.Join(parts, ","), nil
	default:
		return jsjson.Stringify(v)
	}
}
