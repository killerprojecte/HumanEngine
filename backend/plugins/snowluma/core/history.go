package snowluma

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

func historyUnavailable(reason string) any {
	return httpJSON{"kind": "history", "result": makeObject("items", []any{}, "coverage", "unavailable", "reason", reason)}
}
func historyFailure(err error) any {
	if e, ok := err.(protocolError); ok {
		return historyUnavailable(string(e))
	}
	return historyUnavailable("request_failed")
}

// HistoryDecision keeps source scope validation, platform IDs, ordering,
// duplicate replacement, cursor and coverage decisions in the guest.
func HistoryDecision(input *jsjson.Object) any {
	op := textProperty(input, "operation")
	if truthy(property(input, "failed")) {
		if property(input, "oneBot") == true {
			return historyUnavailable(textProperty(input, "code"))
		}
		return historyUnavailable("request_failed")
	}
	var state *jsjson.Object
	if op == "history_begin" || op == "message_begin" {
		scene, err := object(property(input, "scene"))
		if err != nil {
			return historyFailure(err)
		}
		if err = assertSourceScene(textProperty(input, "adapter"), textProperty(input, "account"), scene); err != nil {
			return historyFailure(err)
		}
		state = makeObject("scene", scene, "single", op == "message_begin")
		if v, ok := input.Get("messageId"); ok {
			_ = state.Set("messageId", v)
		}
		if op == "message_begin" {
			return httpJSON{"kind": "is_recalled", "state": state, "sceneId": property(scene, "id"), "messageId": property(input, "messageId")}
		}
		request, err := object(property(input, "input"))
		if err != nil {
			return historyFailure(err)
		}
		limit := toHistoryNumber(property(request, "limit"))
		limit = math.Max(1, math.Min(100, math.Floor(limit)))
		if math.IsNaN(limit) || math.IsInf(limit, 0) {
			return historyUnavailable("invalid_history_limit")
		}
		_ = state.Set("limit", limit)
		cursor := property(request, "cursor")
		if !isMissing(cursor) {
			_ = state.Set("cursor", cursor)
		}
		action, field := "get_friend_msg_history", "user_id"
		if sameString(property(scene, "kind"), "group") {
			action, field = "get_group_msg_history", "group_id"
		}
		count := limit
		if truthy(cursor) {
			count++
		}
		params := makeObject(field, property(scene, "platformSceneId"), "count", count, "reverse_order", true)
		if truthy(cursor) {
			id, err := platformMessage(scene, cursor)
			if err != nil {
				return historyFailure(err)
			}
			_ = params.Set("message_id", id)
		}
		return httpJSON{"kind": "action", "state": state, "action": action, "params": params}
	}
	var err error
	state, err = object(property(input, "state"))
	if err != nil {
		return historyFailure(err)
	}
	if op == "history_sorted" {
		return historySort(state, input, true)
	}
	scene, _ := object(property(state, "scene"))
	single := property(state, "single") == true
	if op == "message_recalled" {
		if property(input, "recalled") == true {
			return historyUnavailable("recalled")
		}
		id, err := platformMessage(scene, property(state, "messageId"))
		if err != nil {
			return historyFailure(err)
		}
		return httpJSON{"kind": "action", "state": state, "action": "get_msg", "params": makeObject("message_id", id)}
	}
	if op == "history_response" {
		value, err := object(property(input, "value"))
		if err != nil {
			return historyFailure(err)
		}
		rows := []any{value}
		if !single {
			var ok bool
			rows, ok = property(value, "messages").([]any)
			if !ok {
				return historyUnavailable("invalid_history_response")
			}
		}
		_ = state.Set("rows", rows)
		_ = state.Set("at", float64(0))
		_ = state.Set("messages", []any{})
	}
	if op == "history_normalized" {
		message, err := object(property(input, "message"))
		if err != nil {
			return historyFailure(err)
		}
		if single {
			if property(message, "id") != property(state, "messageId") {
				return historyUnavailable("message_id_mismatch")
			}
			if sameString(property(message, "status"), "recalled") {
				return historyUnavailable("recalled")
			}
			return httpJSON{"kind": "history", "result": makeObject("items", []any{message}, "coverage", "complete")}
		}
		messages, _ := property(state, "messages").([]any)
		_ = state.Set("messages", append(messages, message))
		at, _ := property(state, "at").(float64)
		_ = state.Set("at", at+1)
	}
	rows, _ := property(state, "rows").([]any)
	at, _ := property(state, "at").(float64)
	if int(at) < len(rows) {
		event, err := object(rows[int(at)])
		if err != nil {
			return historyFailure(err)
		}
		return httpJSON{"kind": "normalize", "state": state, "event": event, "scene": scene}
	}
	if op != "history_sorted" {
		_ = state.Set("sortWidth", float64(1))
		_ = state.Set("sortStart", float64(0))
		_ = state.Set("sortLeft", float64(0))
		_ = state.Set("sortRight", float64(1))
		_ = state.Set("sortOut", []any{})
	}
	return historySort(state, input, op == "history_sorted")
}
func historyFinish(state *jsjson.Object) any {
	messages, _ := property(state, "messages").([]any)
	unique := []any{}
	positions := map[string]int{}
	for _, v := range messages {
		id := textProperty(v.(*jsjson.Object), "id")
		if n, ok := positions[id]; ok {
			unique[n] = v
		} else {
			positions[id] = len(unique)
			unique = append(unique, v)
		}
	}
	filtered := []any{}
	cursor := property(state, "cursor")
	for _, v := range unique {
		if property(v.(*jsjson.Object), "id") != cursor {
			filtered = append(filtered, v)
		}
	}
	result := makeObject("items", []any{}, "coverage", "partial")
	if len(filtered) > 0 {
		_ = result.Set("nextCursor", property(filtered[0].(*jsjson.Object), "id"))
		_ = result.Set("reason", "platform_history_window")
	} else {
		_ = result.Set("reason", "history_exhausted_or_unavailable")
	}
	available := []any{}
	for _, v := range filtered {
		if !sameString(property(v.(*jsjson.Object), "status"), "recalled") {
			available = append(available, v)
		}
	}
	limit, _ := property(state, "limit").(float64)
	if len(available) > int(limit) {
		available = available[len(available)-int(limit):]
	}
	_ = result.Set("items", available)
	return httpJSON{"kind": "history", "result": result}
}
func toHistoryNumber(v any) float64 {
	if n, ok := v.(float64); ok {
		return n
	}
	if v == nil {
		return 0
	}
	if b, ok := v.(bool); ok {
		if b {
			return 1
		}
		return 0
	}
	text, e := jsString(v)
	if e != nil {
		return math.NaN()
	}
	return parseHistoryNumber(text)
}

var historyDecimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func parseHistoryNumber(text string) float64 {
	text = snowTrim(text)
	if text == "" {
		return 0
	}
	if text == "Infinity" || text == "+Infinity" {
		return math.Inf(1)
	}
	if text == "-Infinity" {
		return math.Inf(-1)
	}
	lower := strings.ToLower(text)
	for _, prefix := range []string{"0x", "0b", "0o"} {
		if strings.HasPrefix(lower, prefix) {
			base := 16
			if prefix == "0b" {
				base = 2
			} else if prefix == "0o" {
				base = 8
			}
			n, ok := new(big.Int).SetString(lower[2:], base)
			if !ok || strings.HasPrefix(lower[2:], "+") || strings.HasPrefix(lower[2:], "-") {
				return math.NaN()
			}
			f, _ := new(big.Float).SetInt(n).Float64()
			return f
		}
	}
	if !historyDecimal.MatchString(text) {
		return math.NaN()
	}
	f, e := strconv.ParseFloat(text, 64)
	if e != nil && !math.IsInf(f, 0) {
		return math.NaN()
	}
	return f
}

func historySort(state, input *jsjson.Object, resume bool) any {
	rows, _ := property(state, "messages").([]any)
	width, _ := property(state, "sortWidth").(float64)
	start, _ := property(state, "sortStart").(float64)
	left, _ := property(state, "sortLeft").(float64)
	right, _ := property(state, "sortRight").(float64)
	out, _ := property(state, "sortOut").([]any)
	if resume {
		order, _ := property(input, "comparison").(float64)
		if order <= 0 {
			out = append(out, rows[int(left)])
			left++
		} else {
			out = append(out, rows[int(right)])
			right++
		}
	}
	for width < float64(len(rows)) {
		middle := math.Min(start+width, float64(len(rows)))
		end := math.Min(start+width*2, float64(len(rows)))
		for left < middle && right < end {
			_ = state.Set("sortWidth", width)
			_ = state.Set("sortStart", start)
			_ = state.Set("sortLeft", left)
			_ = state.Set("sortRight", right)
			_ = state.Set("sortOut", out)
			return httpJSON{"kind": "compare", "state": state, "left": property(rows[int(left)].(*jsjson.Object), "sentAt"), "right": property(rows[int(right)].(*jsjson.Object), "sentAt")}
		}
		for left < middle {
			out = append(out, rows[int(left)])
			left++
		}
		for right < end {
			out = append(out, rows[int(right)])
			right++
		}
		start = end
		left = start
		right = math.Min(start+width, float64(len(rows)))
		if start >= float64(len(rows)) {
			rows = out
			out = []any{}
			width *= 2
			start = 0
			left = 0
			right = math.Min(width, float64(len(rows)))
			_ = state.Set("messages", rows)
		}
	}
	return historyFinish(state)
}
