package settingsstore

import (
	"bytes"
	"encoding/json"
	"math"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// BudgetValidationError preserves BudgetSettingsSchema's ordered Zod issues.
// It contains only schema diagnostics, never the supplied settings values.
type BudgetValidationError struct{ issues []any }

func (e *BudgetValidationError) Error() string {
	raw, _ := jsjson.Stringify(e.issues)
	var out bytes.Buffer
	_ = json.Indent(&out, []byte(raw), "", "  ") // Formatting only, no JSON decoding.
	return out.String()
}

func object(members ...jsjson.Member) *jsjson.Object { o, _ := jsjson.NewObject(members...); return o }
func member(key string, value any) jsjson.Member     { return jsjson.Member{Key: key, Value: value} }
func property(value any, key string) any {
	if o, ok := value.(*jsjson.Object); ok && o != nil {
		v, _ := o.Get(key)
		return v
	}
	return nil
}
func typeName(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case *jsjson.Object:
		return "object"
	case float64:
		if math.IsNaN(v) {
			return "NaN"
		}
		if math.IsInf(v, 1) {
			return "Infinity"
		}
		if math.IsInf(v, -1) {
			return "-Infinity"
		}
		return "number"
	default:
		return "undefined"
	}
}
func invalidType(expected, key string, value any) *jsjson.Object {
	issue := object(member("expected", expected), member("code", "invalid_type"))
	received := typeName(value)
	if expected == "number" && (received == "NaN" || received == "Infinity" || received == "-Infinity") {
		_ = issue.Set("received", received)
	}
	path := []any{}
	if key != "" {
		path = append(path, key)
	}
	_ = issue.Set("path", path)
	_ = issue.Set("message", "Invalid input: expected "+expected+", received "+received)
	return issue
}
func numberBound(key string, limit float64, lower bool, safe bool) *jsjson.Object {
	code, bound, adjective, op := "too_big", "maximum", "big", "<="
	if lower {
		code, bound, adjective, op = "too_small", "minimum", "small", ">="
	}
	var issue *jsjson.Object
	origin := "number"
	if safe {
		origin = "int"
		issue = object(member("code", code), member(bound, limit), member("note", "Integers must be within the safe integer range."), member("origin", origin))
	} else {
		issue = object(member("origin", origin), member("code", code), member(bound, limit))
	}
	_ = issue.Set("inclusive", true)
	_ = issue.Set("path", []any{key})
	number, _ := jsjson.Stringify(limit)
	_ = issue.Set("message", "Too "+adjective+": expected "+origin+" to be "+op+number)
	return issue
}

// This is the security projection, not SettingsSchema validation. Store.set
// accepts all other properties unchanged, including unknown keys and null.
// Defaults, field order, bounds and refinements are pinned to src/budget/settings.ts.
func budgetProjection(input any) (*jsjson.Object, error) {
	if input == nil {
		input = object()
	}
	row, ok := input.(*jsjson.Object)
	if !ok {
		return nil, &BudgetValidationError{[]any{invalidType("object", "", input)}}
	}
	fields := []struct {
		key              string
		fallback         any
		minimum, maximum float64
		integer          bool
	}{
		{"budgetExperimentEnabled", false, 0, 0, false},
		{"budgetMode", "strict", 0, 0, false},
		{"budgetDailyUsd", float64(1), 0.000001, 100000, false},
		{"budgetMaxUnresolvedUsd", float64(0.25), 0.000001, 100000, false},
		{"budgetPerTurnUsd", float64(0.1), 0.000001, 100000, false},
		{"budgetPerTurnTokens", float64(32768), 1, 10000000, true},
		{"budgetPerTurnCalls", float64(8), 1, 1000, true},
		{"budgetMaxOutputTokens", float64(2048), 1, 1000000, true},
		{"budgetRealtimeRatio", float64(0.2), 0, 0.5, false},
		{"budgetMaintenanceRatio", float64(0.1), 0, 0.5, false},
	}
	out := object()
	issues := []any{}
	aborted := false
	for _, field := range fields {
		value, present := row.Get(field.key)
		if !present {
			value = field.fallback
		}
		_ = out.Set(field.key, value)
		switch field.fallback.(type) {
		case bool:
			if _, ok := value.(bool); !ok {
				issues = append(issues, invalidType("boolean", field.key, value))
				aborted = true
			}
		case string:
			if value != "strict" && value != "best_effort" {
				issues = append(issues, object(member("code", "invalid_value"), member("values", []any{"strict", "best_effort"}), member("path", []any{field.key}), member("message", `Invalid option: expected one of "strict"|"best_effort"`)))
				aborted = true
			}
		case float64:
			n, ok := value.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
				issues = append(issues, invalidType("number", field.key, value))
				aborted = true
				continue
			}
			if field.integer {
				if math.Trunc(n) != n {
					issues = append(issues, object(member("expected", "int"), member("format", "safeint"), member("code", "invalid_type"), member("path", []any{field.key}), member("message", "Invalid input: expected int, received number")))
					aborted = true
					continue
				}
				if n > maxSafeInteger {
					issues = append(issues, numberBound(field.key, maxSafeInteger, false, true))
				}
				if n < -maxSafeInteger {
					issues = append(issues, numberBound(field.key, -maxSafeInteger, true, true))
				}
			}
			if n < field.minimum {
				issues = append(issues, numberBound(field.key, field.minimum, true, false))
			}
			if n > field.maximum {
				issues = append(issues, numberBound(field.key, field.maximum, false, false))
			}
		}
	}
	// Zod's non-continuable type errors skip superRefine, whereas bounds do not.
	if !aborted {
		number := func(key string) float64 { return property(out, key).(float64) }
		refine := func(key, message string) {
			issues = append(issues, object(member("code", "custom"), member("path", []any{key}), member("message", message)))
		}
		if number("budgetRealtimeRatio")+number("budgetMaintenanceRatio") > 0.5 {
			refine("budgetMaintenanceRatio", "Reserve at least 50% of the daily budget for foreground requests")
		}
		if number("budgetMaxUnresolvedUsd") > number("budgetDailyUsd") {
			refine("budgetMaxUnresolvedUsd", "Unresolved limit must not exceed daily limit")
		}
		if number("budgetPerTurnUsd") > number("budgetDailyUsd") {
			refine("budgetPerTurnUsd", "Turn limit must not exceed daily limit")
		}
	}
	if len(issues) > 0 {
		return nil, &BudgetValidationError{issues}
	}
	return out, nil
}
