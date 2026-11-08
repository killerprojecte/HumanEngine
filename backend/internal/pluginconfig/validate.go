package pluginconfig

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
)

// ValidateBuiltin validates the source form vocabulary. Pattern support is
// deliberately limited to the two exact expressions shipped in V2 manifests.
// This is NOT an arbitrary V1 JavaScript RegExp implementation.
func ValidateBuiltin(value any, schema *jsjson.Object) error {
	fail := func(code string) error { return errors.New(code) }
	if schema == nil {
		return fail("unsupported_schema_type")
	}
	allowed := map[string]bool{"type": true, "properties": true, "required": true, "additionalProperties": true, "description": true, "title": true, "default": true, "enum": true, "minimum": true, "maximum": true, "pattern": true, "minLength": true, "maxLength": true, "items": true, "secret": true, "group": true, "$schema": true}
	for _, key := range schema.Keys() {
		if !allowed[key] {
			return fail("unsupported_schema_keyword")
		}
	}
	if enum, ok := field(schema, "enum").([]any); ok {
		found := false
		for _, candidate := range enum {
			if sameValueZero(value, candidate) {
				found = true
				break
			}
		}
		if !found {
			return fail("invalid_enum")
		}
	}
	switch field(schema, "type") {
	case "object":
		obj := object(value)
		if obj == nil {
			return fail("expected_object")
		}
		properties := object(field(schema, "properties"))
		if required, ok := field(schema, "required").([]any); ok {
			for _, v := range required {
				key, ok := v.(string)
				if !ok {
					return fail("required_property")
				}
				if _, has := obj.Get(key); !has {
					return fail("required_property")
				}
			}
		}
		for _, m := range obj.Members() {
			child := object(field(properties, m.Key))
			if child == nil {
				if field(schema, "additionalProperties") != true {
					return fail("unknown_property")
				}
			} else if err := ValidateBuiltin(m.Value, child); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fail("expected_string")
		}
		units, err := jsjson.UTF16(text)
		if err != nil {
			return err
		}
		if min, ok := field(schema, "minLength").(float64); ok && float64(len(units)) < min {
			return fail("expected_string")
		}
		if max, ok := field(schema, "maxLength").(float64); ok && float64(len(units)) > max {
			return fail("expected_string")
		}
		if pattern, ok := field(schema, "pattern").(string); ok {
			matches, err := builtinPattern(pattern, units)
			if err != nil {
				return err
			}
			if !matches {
				return fail("expected_string")
			}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fail("expected_boolean")
		}
	case "integer", "number":
		n, ok := value.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || (field(schema, "type") == "integer" && math.Trunc(n) != n) {
			return fail("expected_number")
		}
		if min, ok := field(schema, "minimum").(float64); ok && n < min {
			return fail("expected_number")
		}
		if max, ok := field(schema, "maximum").(float64); ok && n > max {
			return fail("expected_number")
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return fail("expected_array")
		}
		for _, v := range values {
			if err := ValidateBuiltin(v, object(field(schema, "items"))); err != nil {
				return err
			}
		}
	default:
		return fail("unsupported_schema_type")
	}
	return nil
}
func sameValueZero(a, b any) bool {
	switch a := a.(type) {
	case nil:
		return b == nil
	case bool:
		v, ok := b.(bool)
		return ok && a == v
	case string:
		v, ok := b.(string)
		return ok && a == v
	case float64:
		v, ok := b.(float64)
		return ok && (a == v || math.IsNaN(a) && math.IsNaN(v))
	case *jsjson.Object:
		v, ok := b.(*jsjson.Object)
		return ok && a == v
	}
	return false
}
func builtinPattern(pattern string, units []uint16) (bool, error) {
	// These source expressions have no multiline flag; $ is the input end.
	upper := func(c uint16) bool { return c >= 'A' && c <= 'Z' }
	digit := func(c uint16) bool { return c >= '0' && c <= '9' }
	switch pattern {
	case "^[A-Z][A-Z0-9_]*$":
		if len(units) == 0 || !upper(units[0]) {
			return false, nil
		}
		for _, c := range units[1:] {
			if !upper(c) && !digit(c) && c != '_' {
				return false, nil
			}
		}
		return true, nil
	case `^\/[a-zA-Z0-9/_-]+$`:
		if len(units) < 2 || units[0] != '/' {
			return false, nil
		}
		for _, c := range units[1:] {
			if !upper(c) && !(c >= 'a' && c <= 'z') && !digit(c) && c != '/' && c != '_' && c != '-' {
				return false, nil
			}
		}
		return true, nil
	default:
		return false, errors.New("unsupported_builtin_schema_pattern")
	}
}
