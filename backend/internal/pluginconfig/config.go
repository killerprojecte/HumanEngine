// Package pluginconfig implements the source plugin form's JSON transformations.
// Values use jsjson so schema property order and lone UTF-16 surrogates survive.
package pluginconfig

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"strconv"
)

func field(schema *jsjson.Object, key string) any {
	if schema == nil {
		return nil
	}
	value, _ := schema.Get(key)
	return value
}
func object(value any) *jsjson.Object { result, _ := value.(*jsjson.Object); return result }
func clone(value any) any {
	switch v := value.(type) {
	case *jsjson.Object:
		out := new(jsjson.Object)
		for _, m := range v.Members() {
			_ = out.Set(m.Key, clone(m.Value))
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, entry := range v {
			out[i] = clone(entry)
		}
		return out
	default:
		return value
	}
}

// Redact returns present=false for a secret root. A secret array element becomes
// null; a secret object property is omitted, including explicitly null secrets.
func Redact(value any, schema *jsjson.Object) (safe any, present bool) {
	if field(schema, "secret") == true {
		return nil, false
	}
	if field(schema, "type") == "object" {
		if value := object(value); value != nil {
			properties := object(field(schema, "properties"))
			out := new(jsjson.Object)
			for _, m := range value.Members() {
				child := object(field(properties, m.Key))
				next, keep := clone(m.Value), true
				if child != nil {
					next, keep = Redact(m.Value, child)
				}
				if keep {
					_ = out.Set(m.Key, next)
				}
			}
			return out, true
		}
	}
	if field(schema, "type") == "array" {
		if values, ok := value.([]any); ok {
			out := make([]any, len(values))
			for i, entry := range values {
				out[i], _ = Redact(entry, object(field(schema, "items")))
			}
			return out, true
		}
	}
	return clone(value), true
}

// Preserve distinguishes missing properties from JSON null. Only secret fields
// use the old value when omitted or null. Ordinary omitted fields are not merged.
func Preserve(value any, present bool, previous any, previousPresent bool, schema *jsjson.Object) (any, bool) {
	if field(schema, "secret") == true {
		if !present || value == nil {
			return clone(previous), previousPresent
		}
		return clone(value), true
	}
	if field(schema, "type") == "object" {
		if incoming := object(value); incoming != nil && present {
			out := clone(incoming).(*jsjson.Object)
			old := previous
			properties := object(field(schema, "properties"))
			if properties != nil {
				for _, m := range properties.Members() {
					v, has := out.Get(m.Key)
					var prev any
					var had bool
					if old != nil {
						prev, had = previousField(old, m.Key)
					}
					if !has && !had {
						continue
					}
					next, keep := Preserve(v, has, prev, had, object(m.Value))
					if keep {
						_ = out.Set(m.Key, next)
					}
				}
			}
			return out, true
		}
	}
	if field(schema, "type") == "array" {
		if values, ok := value.([]any); ok && present {
			old, _ := previous.([]any)
			out := make([]any, len(values))
			for i, v := range values {
				var prev any
				had := i < len(old)
				if had {
					prev = old[i]
				}
				out[i], _ = Preserve(v, true, prev, had, object(field(schema, "items")))
			}
			return out, true
		}
	}
	return clone(value), present
}

// PublicSchema removes secrets from defaults at every schema depth, without
// changing UI form ordering or stripping the secret marker itself.
func PublicSchema(schema *jsjson.Object) *jsjson.Object {
	if schema == nil {
		return new(jsjson.Object)
	}
	result := clone(schema).(*jsjson.Object)
	if value, ok := result.Get("default"); ok {
		if safe, keep := Redact(value, result); keep {
			_ = result.Set("default", safe)
		} else {
			result.Delete("default")
		}
	}
	if properties := object(field(result, "properties")); properties != nil {
		for _, m := range properties.Members() {
			_ = properties.Set(m.Key, PublicSchema(object(m.Value)))
		}
	}
	if items := object(field(result, "items")); items != nil {
		_ = result.Set("items", PublicSchema(items))
	}
	return result
}

// Source treats an old array as an object when preserving an incoming object.
func previousField(value any, key string) (any, bool) {
	if o := object(value); o != nil {
		return o.Get(key)
	}
	if a, ok := value.([]any); ok {
		if key == "length" {
			return float64(len(a)), true
		}
		i, err := strconv.Atoi(key)
		if err == nil && i >= 0 && i < len(a) && strconv.Itoa(i) == key {
			return a[i], true
		}
	}
	return nil, false
}
