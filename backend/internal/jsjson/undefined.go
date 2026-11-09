package jsjson

import "errors"

// undefinedValue is deliberately not constructible by Parse or from JSON null.
// Its zero-field representation has one semantic value and is safely copied by
// the existing native structured clones. It is not an object or a user string.
type undefinedValue struct{}

// UndefinedValue aliases the dedicated zero-field value so existing domain
// adapters can recognize it without introducing another undefined identity.
type UndefinedValue = undefinedValue

// Undefined models an own property whose JavaScript value is undefined. Unlike
// deleting that property, it remains visible to Get, Len, Members and equality.
// JSON.stringify omits such object members and encodes array elements as null.
var Undefined = undefinedValue{}

// ErrUndefinedRoot means JSON.stringify produced no JSON text. Stringify and
// Marshal cannot represent that as a JSON string/byte result, so they return an
// explicit error rather than silently encoding null, an empty string, or {}.
var ErrUndefinedRoot = errors.New("jsjson: undefined has no JSON text")

func IsUndefined(value any) bool { _, ok := value.(undefinedValue); return ok }
