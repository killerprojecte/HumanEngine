package providerwire

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type StreamError struct{ Message, Code string }

func (e *StreamError) Error() string { return e.Message }
func streamFail(m string, codes ...string) {
	code := m
	if len(codes) > 0 {
		code = codes[0]
	}
	panic(&StreamError{m, code})
}
func invalid() { streamFail("invalid_provider_stream") }
func so(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		invalid()
	}
	return m
}
func sl(v any) []any {
	a, ok := v.([]any)
	if !ok {
		invalid()
	}
	return a
}
func ss(v any) string {
	s, ok := v.(string)
	if !ok {
		invalid()
	}
	return s
}
func si(v any) int {
	n, ok := v.(float64)
	if !ok || math.Trunc(n) != n || n < 0 || n >= 65536 {
		invalid()
	}
	return int(n)
}
func appendStream(m map[string]any, k string, v any, progress func()) {
	s := ss(v)
	old := ""
	if m[k] != nil {
		old = ss(m[k])
	}
	a, ae := jsjson.UTF16(old)
	b, be := jsjson.UTF16(s)
	if ae != nil || be != nil {
		invalid()
	}
	m[k] = jsjson.FromUTF16(append(a, b...))
	if trimJS(s) != "" {
		progress()
	}
}
func containsStream(final, partial any) bool {
	if partial == nil {
		return true
	}
	switch p := partial.(type) {
	case string:
		f, ok := final.(string)
		return ok && prefixJS(f, p)
	case []any:
		f, ok := final.([]any)
		if !ok || len(f) < len(p) {
			return false
		}
		for i, v := range p {
			if !containsStream(f[i], v) {
				return false
			}
		}
		return true
	case map[string]any:
		f, ok := final.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range p {
			if k == "status" {
				continue
			}
			switch k {
			case "id", "call_id", "type", "name", "role":
				if !reflect.DeepEqual(f[k], v) {
					return false
				}
			default:
				if !containsStream(f[k], v) {
					return false
				}
			}
		}
		return true
	}
	return reflect.DeepEqual(final, partial)
}
func validateStreamArguments(v any) {
	defer func() {
		if recover() != nil {
			streamFail("incomplete_model_response")
		}
	}()
	x, e := parseStreamJSON(ss(v))
	if e != nil {
		streamFail("incomplete_model_response")
	}
	if _, ok := x.(map[string]any); !ok {
		streamFail("incomplete_model_response")
	}
}

type chatStream struct {
	result   map[string]any
	choices  map[int]map[string]any
	tools    map[int]map[int]map[string]any
	progress func(string)
}

func (c *chatStream) event(d map[string]any) {
	if truth(d["error"]) {
		streamFail("provider_stream_error", "provider_stream_upstream_error")
	}
	if _, ok := d["choices"].([]any); !ok {
		if t, ok := d["type"].(string); ok && strings.HasPrefix(t, "response.") {
			streamFail("invalid_provider_stream", "provider_stream_protocol_mismatch")
		}
	}
	for _, k := range []string{"id", "model", "created", "system_fingerprint", "service_tier"} {
		if v, ok := d[k]; ok {
			if old, exists := c.result[k]; k == "id" && exists && !reflect.DeepEqual(old, v) {
				invalid()
			}
			c.result[k] = v
		}
	}
	if d["usage"] != nil {
		c.result["usage"] = so(d["usage"])
	}
	for _, raw := range sl(d["choices"]) {
		chunk := so(raw)
		idx := si(chunk["index"])
		choice := c.choices[idx]
		if choice == nil {
			choice = map[string]any{"index": float64(idx), "message": map[string]any{"role": "assistant"}, "finish_reason": nil}
			c.choices[idx] = choice
		}
		if choice["finish_reason"] != nil {
			invalid()
		}
		delta := so(chunk["delta"])
		message := so(choice["message"])
		if role, ok := delta["role"]; ok && role != "assistant" {
			invalid()
		}
		for _, k := range []string{"content", "reasoning_content", "reasoning", "refusal"} {
			if delta[k] != nil {
				kind := "text"
				if strings.HasPrefix(k, "reasoning") {
					kind = "reasoning"
				}
				appendStream(message, k, delta[k], func() { c.progress(kind) })
			} else if v, ok := delta[k]; k == "content" && ok && v == nil {
				if _, exists := message[k]; !exists {
					message[k] = nil
				}
			}
		}
		if raw, ok := delta["tool_calls"]; ok {
			calls := c.tools[idx]
			if calls == nil {
				calls = map[int]map[string]any{}
				c.tools[idx] = calls
			}
			for _, raw := range sl(raw) {
				part := so(raw)
				i := si(part["index"])
				call := calls[i]
				if call == nil {
					call = map[string]any{"function": map[string]any{}}
					calls[i] = call
				}
				for _, k := range []string{"id", "type"} {
					if v, ok := part[k]; ok {
						if old, exists := call[k]; exists && !reflect.DeepEqual(old, v) {
							invalid()
						}
						call[k] = ss(v)
					}
				}
				if raw, ok := part["function"]; ok {
					fn := so(raw)
					target := so(call["function"])
					if v, ok := fn["name"]; ok {
						appendStream(target, "name", v, func() {})
					}
					if v, ok := fn["arguments"]; ok {
						appendStream(target, "arguments", v, func() { c.progress("tool") })
					}
				}
			}
		}
		if chunk["finish_reason"] != nil {
			choice["finish_reason"] = ss(chunk["finish_reason"])
		}
	}
}
func sortedStreamKeys[T any](m map[int]T) []int {
	a := make([]int, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	sort.Ints(a)
	return a
}
func (c *chatStream) finish() map[string]any {
	if c.choices[0] == nil {
		streamFail("incomplete_model_response")
	}
	choices := []any{}
	for _, i := range sortedStreamKeys(c.choices) {
		choice := c.choices[i]
		if choice["finish_reason"] != "stop" && choice["finish_reason"] != "tool_calls" {
			streamFail("incomplete_model_response")
		}
		calls, exists := c.tools[i]
		if exists {
			a := []any{}
			for pos, j := range sortedStreamKeys(calls) {
				call := calls[j]
				if j != pos || !truth(call["id"]) || call["type"] != "function" || !truth(so(call["function"])["name"]) {
					invalid()
				}
				validateStreamArguments(so(call["function"])["arguments"])
				a = append(a, call)
			}
			so(choice["message"])["tool_calls"] = a
		}
		if choice["finish_reason"] == "tool_calls" && len(calls) == 0 {
			streamFail("incomplete_model_response")
		}
		choices = append(choices, choice)
	}
	c.result["object"] = "chat.completion"
	c.result["choices"] = choices
	return c.result
}

type responsesStream struct {
	items    map[int]map[string]any
	lengths  map[string]int
	id       any
	hasID    bool
	progress func(string)
}

func (r *responsesStream) observe(index int, item map[string]any) {
	record := func(path string, value any, kind string) {
		s, ok := value.(string)
		if !ok || s == "" {
			return
		}
		key := strconv.Itoa(index) + ":" + path
		old := r.lengths[key]
		units, _ := jsjson.UTF16(s)
		if len(units) <= old {
			return
		}
		r.lengths[key] = len(units)
		if trimJS(jsjson.FromUTF16(units[old:])) != "" {
			r.progress(kind)
		}
	}
	if item["type"] == "function_call" {
		record("arguments", item["arguments"], "tool")
	}
	if item["type"] != "message" && item["type"] != "reasoning" {
		return
	}
	for _, k := range []string{"content", "summary"} {
		parts, ok := item[k].([]any)
		if !ok {
			continue
		}
		for i, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			kind := "text"
			if item["type"] == "reasoning" {
				kind = "reasoning"
			}
			record(k+":"+strconv.Itoa(i)+":text", part["text"], kind)
			record(k+":"+strconv.Itoa(i)+":refusal", part["refusal"], kind)
		}
	}
}
func (r *responsesStream) event(d map[string]any) map[string]any {
	if _, ok := d["type"]; !ok {
		if _, ok := d["choices"].([]any); ok {
			streamFail("invalid_provider_stream", "provider_stream_protocol_mismatch")
		}
	}
	t := ss(d["type"])
	if t == "error" || t == "response.failed" || t == "response.incomplete" {
		streamFail("provider_stream_error", "provider_stream_upstream_error")
	}
	if t == "response.created" || t == "response.in_progress" || t == "response.completed" {
		res := so(d["response"])
		if r.hasID && !reflect.DeepEqual(r.id, res["id"]) {
			invalid()
		}
		r.id = res["id"]
		_, r.hasID = res["id"]
		if t != "response.completed" {
			return nil
		}
		if res["status"] != "completed" || truth(res["error"]) || truth(res["incomplete_details"]) {
			streamFail("incomplete_model_response")
		}
		output := sl(res["output"])
		for i, partial := range r.items {
			if i >= len(output) || !containsStream(output[i], partial) {
				invalid()
			}
		}
		for i, raw := range output {
			item := so(raw)
			if item["type"] == "function_call" {
				validateStreamArguments(item["arguments"])
			}
			if status, ok := item["status"]; ok && status != "completed" {
				streamFail("incomplete_model_response")
			}
			r.observe(i, item)
		}
		return res
	}
	if t == "response.output_item.added" || t == "response.output_item.done" {
		i := si(d["output_index"])
		item := so(d["item"])
		if prev := r.items[i]; prev != nil && (strings.HasSuffix(t, ".added") || !containsStream(item, prev)) {
			invalid()
		}
		r.items[i] = item
		r.observe(i, item)
		return nil
	}
	fields := map[string][3]string{"response.output_text.delta": {"content", "text", "content_index"}, "response.refusal.delta": {"content", "refusal", "content_index"}, "response.reasoning_summary_text.delta": {"summary", "text", "summary_index"}, "response.reasoning_text.delta": {"content", "text", "content_index"}, "response.function_call_arguments.delta": {"", "arguments", ""}}
	field, ok := fields[t]
	done := false
	if strings.HasSuffix(t, ".done") {
		field, ok = fields[strings.TrimSuffix(t, ".done")+".delta"]
		done = ok
	}
	added := t == "response.content_part.added" || t == "response.reasoning_summary_part.added"
	partDone := t == "response.content_part.done" || t == "response.reasoning_summary_part.done"
	if !ok && !added && !partDone {
		return nil
	}
	i := si(d["output_index"])
	item := r.items[i]
	if item == nil {
		invalid()
	}
	if id, ok := d["item_id"]; ok && !reflect.DeepEqual(item["id"], id) {
		invalid()
	}
	if added || partDone {
		k, indexKey := "content", "content_index"
		if strings.Contains(t, "summary") {
			k, indexKey = "summary", "summary_index"
		}
		parts := []any{}
		if v, exists := item[k]; exists {
			parts = sl(v)
		}
		idx := si(d[indexKey])
		if partDone {
			if idx >= len(parts) || !containsStream(d["part"], parts[idx]) {
				invalid()
			}
			parts[idx] = so(d["part"])
		} else {
			if idx != len(parts) {
				invalid()
			}
			parts = append(parts, so(d["part"]))
		}
		item[k] = parts
	} else {
		target := item
		if field[2] != "" {
			parts := sl(item[field[0]])
			idx := si(d[field[2]])
			if idx >= len(parts) {
				invalid()
			}
			target = so(parts[idx])
		}
		if done {
			v := d[field[1]]
			partial, exists := target[field[1]]
			if !exists {
				partial = streamUndefined{}
			}
			if !containsStream(v, partial) {
				invalid()
			}
			target[field[1]] = ss(v)
		} else {
			appendStream(target, field[1], d["delta"], func() {})
		}
	}
	r.observe(i, item)
	return nil
}

// ReadStream consumes SSE and closes the response body. Limits match the source protocol.
func ReadStream(body io.ReadCloser, protocol string, progress func(string)) (result map[string]any, err error) {
	defer func() {
		if body != nil {
			_ = body.Close()
		}
		if p := recover(); p != nil {
			result = nil
			if e, ok := p.(*StreamError); ok {
				err = e
			} else {
				err = &StreamError{"provider_stream_error", "provider_stream_transport_error"}
			}
		}
	}()
	if body == nil {
		streamFail("incomplete_model_response")
	}
	if progress == nil {
		progress = func(string) {}
	}
	chat := chatStream{map[string]any{}, map[int]map[string]any{}, map[int]map[int]map[string]any{}, progress}
	responses := responsesStream{items: map[int]map[string]any{}, lengths: map[string]int{}, progress: progress}
	line := ""
	data := []string{}
	eventName := ""
	skipLF := false
	bytesRead := 0
	firstDecoded := true
	pending := []byte{}
	buf := make([]byte, 32768)
	dispatch := func() {
		if len(data) == 0 {
			eventName = ""
			return
		}
		payload := strings.Join(data, "\n")
		data = nil
		event := eventName
		eventName = ""
		if event == "error" {
			streamFail("provider_stream_error", "provider_stream_upstream_error")
		}
		if payload == "[DONE]" {
			if protocol != "openai-chat" {
				streamFail("incomplete_model_response")
			}
			result = chat.finish()
			return
		}
		raw, e := parseStreamJSON(payload)
		if e != nil {
			streamFail("invalid_provider_stream", "provider_stream_invalid_json")
		}
		parsed := so(raw)
		if protocol == "openai-chat" {
			chat.event(parsed)
		} else {
			if event != "" && parsed["type"] != event {
				streamFail("invalid_provider_stream", "provider_stream_event_mismatch")
			}
			result = responses.event(parsed)
		}
	}
	consume := func() {
		current := line
		line = ""
		if current == "" {
			dispatch()
			return
		}
		if strings.HasPrefix(current, ":") {
			return
		}
		field, value, found := strings.Cut(current, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		if field == "data" {
			data = append(data, value)
		} else if field == "event" {
			eventName = value
		}
	}
	for result == nil {
		n, readErr := body.Read(buf)
		if n > 0 {
			bytesRead += n
			if bytesRead > 8*1024*1024 {
				streamFail("provider_response_too_large")
			}
			pending = append(pending, buf[:n]...)
			end := len(pending)
			for end > 0 && !utf8.Valid(pending[:end]) {
				if len(pending)-end >= 3 {
					streamFail("provider_stream_error", "provider_stream_invalid_utf8")
				}
				end--
			}
			if end < len(pending) && utf8.FullRune(pending[end:]) {
				streamFail("provider_stream_error", "provider_stream_invalid_utf8")
			}
			text := string(pending[:end])
			if firstDecoded && text != "" {
				text = strings.TrimPrefix(text, "\ufeff")
				firstDecoded = false
			}
			pending = append(pending[:0], pending[end:]...)
			for _, ch := range text {
				if skipLF && ch == '\n' {
					skipLF = false
					continue
				}
				skipLF = false
				if ch == '\r' || ch == '\n' {
					skipLF = ch == '\r'
					consume()
					if result != nil {
						break
					}
				} else {
					line += string(ch)
				}
			}
		}
		if result != nil {
			return result, nil
		}
		if readErr != nil {
			if readErr == io.EOF {
				if len(pending) > 0 {
					streamFail("provider_stream_error", "provider_stream_invalid_utf8")
				}
				streamFail("incomplete_model_response")
			}
			streamFail("provider_stream_error", "provider_stream_transport_error")
		}
	}
	return result, nil
}
func trimJS(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r == '\u0009' || r == '\u000b' || r == '\u000c' || r == '\u0020' || r == '\u00a0' || r == '\ufeff' || r == '\u000a' || r == '\u000d' || r == '\u2028' || r == '\u2029' || r == '\u1680' || r >= '\u2000' && r <= '\u200a' || r == '\u202f' || r == '\u205f' || r == '\u3000'
	})
}
func prefixJS(s, p string) bool {
	a, e := jsjson.UTF16(s)
	if e != nil {
		return false
	}
	b, e := jsjson.UTF16(p)
	if e != nil || len(a) < len(b) {
		return false
	}
	for i, n := range b {
		if a[i] != n {
			return false
		}
	}
	return true
}
func parseStreamJSON(s string) (any, error) {
	v, e := jsjson.Parse(s)
	if e != nil {
		return nil, e
	}
	return streamPlainValue(v), nil
}
func streamPlainValue(v any) any {
	switch x := v.(type) {
	case *jsjson.Object:
		m := map[string]any{}
		for _, p := range x.Members() {
			m[p.Key] = streamPlainValue(p.Value)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = streamPlainValue(v)
		}
		return a
	}
	return v
}

type streamUndefined struct{}
