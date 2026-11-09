package snowluma

import (
	"math"
	"sort"
	"strconv"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// HTTPDecision stays in the actual guest: the native port supplies authenticated
// bytes, but cannot substitute its own OneBot success/error interpretation.
func HTTPDecision(operation string, input *jsjson.Object) any {
	get := func(key string) any { value, _ := input.Get(key); return value }
	raw, _ := get("raw").(string)
	parsed, err := jsjson.Parse(raw)
	object, ok := parsed.(*jsjson.Object)
	failure := func(code string, transmitted bool) any {
		return httpJSON{"error": code, "transmitted": transmitted}
	}
	if operation == "http_event" {
		if err != nil || !ok {
			return httpJSON{"httpStatus": 400}
		}
		self, _ := object.Get("self_id")
		id, valid := RawID(self)
		if !valid {
			return httpJSON{"httpStatus": 400}
		}
		if id != get("account") {
			return httpJSON{"httpStatus": 403}
		}
		return httpJSON{"httpStatus": 204, "event": object}
	}
	if operation == "http_response" {
		if err != nil || !ok {
			return failure("invalid_http_response", true)
		}
		status, _ := object.Get("status")
		retcode, _ := object.Get("retcode")
		code, number := retcode.(float64)
		state, isString := status.(string)
		if !number || !isString {
			return failure("invalid_http_response", true)
		}
		if state != "ok" || code != 0 {
			if state != "failed" || code == 0 {
				return failure("invalid_http_response", true)
			}
			reason := "platform_action_failed"
			if code == 1404 {
				reason = "unsupported"
			} else if code == 1400 {
				reason = "invalid_action_parameters"
			}
			return failure(reason, false)
		}
		data, exists := object.Get("data")
		return httpJSON{"data": data, "hasData": exists}
	}
	if operation == "http_login" {
		if err != nil || !ok {
			return failure("invalid_payload", false)
		}
		self, _ := object.Get("user_id")
		id, valid := RawID(self)
		if !valid {
			return failure("invalid_identifier", false)
		}
		if id != get("account") {
			return failure("account_mismatch", false)
		}
		return httpJSON{"account": id}
	}
	return failure("unknown_plugin_operation", false)
}

func RawID(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		units, err := jsjson.UTF16(value)
		return value, err == nil && len(units) > 0 && len(units) <= 256
	case float64:
		if !math.IsNaN(value) && !math.IsInf(value, 0) && math.Trunc(value) == value && math.Abs(value) <= 9007199254740991 {
			if value == 0 {
				return "0", true
			}
			return strconv.FormatFloat(value, 'f', 0, 64), true
		}
	}
	return "", false
}

// The guest ABI uses encoding/json, while parsed JS values intentionally retain
// ordered Objects and lone UTF-16 surrogates. Do not marshal Object as a Go struct.
type httpJSON map[string]any

func (value httpJSON) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	object := new(jsjson.Object)
	for _, key := range keys {
		item := value[key]
		if number, ok := item.(int); ok {
			item = float64(number)
		}
		if err := object.Set(key, item); err != nil {
			return nil, err
		}
	}
	return jsjson.Marshal(object)
}
