package replyplan

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// Undefined represents a JS undefined extension result or optional property.
// nil represents JSON null. JSON serialization omits undefined object fields
// and emits null for undefined array elements, exactly as JSON.stringify does.
var Undefined any = jsjson.Undefined

type undefinedValue = jsjson.UndefinedValue

func isUndefined(v any) bool         { _, ok := v.(undefinedValue); return ok }
func defined(v any, key string) bool { return has(v, key) && !isUndefined(get(v, key)) }
func stringify(value any) (string, error) {
	if isUndefined(value) {
		return "", nil
	}
	return jsjson.Stringify(jsonValue(value))
}
func jsonValue(v any) any {
	switch x := v.(type) {
	case undefinedValue:
		return nil
	case *Object:
		o := obj()
		for _, m := range x.Members() {
			if !isUndefined(m.Value) {
				set(o, m.Key, jsonValue(m.Value))
			}
		}
		return o
	case []any:
		a := make([]any, len(x))
		for i, item := range x {
			a[i] = jsonValue(item)
		}
		return a
	default:
		return v
	}
}
