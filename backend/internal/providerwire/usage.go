package providerwire

import (
	"encoding/json"
	"math"
	"strings"
)

var cacheKeys = []string{"ordinaryInputTokens", "cacheReadInputTokens", "cacheWriteInputTokens", "cacheWrite5mInputTokens", "cacheWrite1hInputTokens", "cacheWrite30mInputTokens", "cacheWrite12hInputTokens"}
var rawPaths = []string{"input_tokens", "output_tokens", "total_tokens", "prompt_tokens", "completion_tokens", "input_tokens_details.cached_tokens", "input_tokens_details.cache_write_tokens", "input_tokens_details.cache_write_tokens_details.ttl_30m_tokens", "input_tokens_details.cache_write_tokens_details.ttl_12h_tokens", "prompt_tokens_details.cached_tokens", "prompt_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens_details.ttl_30m_tokens", "prompt_tokens_details.cache_write_tokens_details.ttl_12h_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens"}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, e := n.Float64()
		return f, e == nil
	}
	return 0, false
}
func validCount(n float64) bool   { return n >= 0 && n <= 9007199254740991 && math.Trunc(n) == n }
func count(v any) (float64, bool) { n, ok := number(v); return n, ok && validCount(n) }
func at(v any, path string) (any, bool) {
	for _, k := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[k]
		if !ok {
			return nil, false
		}
	}
	return v, true
}
func val(m Object, k string) float64 { n, _ := number(m[k]); return n }
func NormalizeProviderUsage(value any, convention string, embedding bool) Object {
	u, _ := obj(value)
	invalid := false
	read := func(paths ...string) *float64 {
		var found *float64
		for _, p := range paths {
			v, ok := at(u, p)
			if !ok {
				continue
			}
			n, ok := count(v)
			if !ok {
				invalid = true
				continue
			}
			if found != nil && *found != n {
				invalid = true
			}
			if found == nil {
				x := n
				found = &x
			}
		}
		return found
	}
	ip, op := []string{"input_tokens"}, []string{"output_tokens"}
	if convention != "anthropic" {
		ip = append(ip, "prompt_tokens")
		op = append(op, "completion_tokens")
	}
	input, output, total := read(ip...), read(op...), read("total_tokens")
	r := Object{"inputTokens": float64(0), "outputTokens": float64(0), "totalTokens": float64(0)}
	if input != nil {
		r["inputTokens"] = *input
	}
	if output != nil {
		r["outputTokens"] = *output
	}
	if input == nil || !embedding && output == nil {
		invalid = true
	}
	has := func(keys ...string) bool {
		for _, k := range keys {
			if _, ok := u[k]; ok {
				return true
			}
		}
		return false
	}
	anth := has("cache_read_input_tokens", "cache_creation_input_tokens", "cache_creation")
	hasCache := anth || has("input_tokens_details", "prompt_tokens_details", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens")
	if hasCache {
		r["cacheKnown"] = false
		raw := Object{}
		for _, p := range rawPaths {
			v, ok := at(u, p)
			if n, valid := count(v); ok && valid {
				raw[p] = n
			}
		}
		r["rawTokenCounters"] = raw
		var cached, written *float64
		if convention == "anthropic" {
			cached, written = read("cache_read_input_tokens"), read("cache_creation_input_tokens")
			five, hour := read("cache_creation.ephemeral_5m_input_tokens"), read("cache_creation.ephemeral_1h_input_tokens")
			if five != nil {
				r["cacheWrite5mInputTokens"] = *five
			}
			if hour != nil {
				r["cacheWrite1hInputTokens"] = *hour
			}
			if written == nil && five != nil && hour != nil {
				x := *five + *hour
				written = &x
			}
			if written != nil && five != nil && hour != nil && *five+*hour != *written {
				invalid = true
			}
			if input != nil && cached != nil && written != nil {
				r["inputTokens"] = *input + *cached + *written
			} else {
				invalid = true
			}
		} else {
			if anth {
				invalid = true
			}
			cached = read("input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "prompt_cache_hit_tokens")
			written = read("input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens")
			half, day := read("input_tokens_details.cache_write_tokens_details.ttl_30m_tokens", "prompt_tokens_details.cache_write_tokens_details.ttl_30m_tokens"), read("input_tokens_details.cache_write_tokens_details.ttl_12h_tokens", "prompt_tokens_details.cache_write_tokens_details.ttl_12h_tokens")
			if half != nil {
				r["cacheWrite30mInputTokens"] = *half
			}
			if day != nil {
				r["cacheWrite12hInputTokens"] = *day
			}
			if written != nil && half != nil && day != nil && *half+*day != *written {
				invalid = true
			}
			miss := read("prompt_cache_miss_tokens")
			if miss != nil && cached != nil {
				if input == nil || *cached+*miss != *input || written != nil && *written != 0 {
					invalid = true
				}
				if written == nil {
					x := float64(0)
					written = &x
				}
			}
		}
		if cached != nil {
			r["cacheReadInputTokens"] = *cached
		}
		if written != nil {
			r["cacheWriteInputTokens"] = *written
		}
		typed := float64(0)
		for _, k := range cacheKeys[3:] {
			typed += val(r, k)
		}
		if !validCount(typed) || typed > val(r, "inputTokens") || written != nil && typed > *written {
			invalid = true
		}
		if val(r, "cacheReadInputTokens")+val(r, "cacheWriteInputTokens") > val(r, "inputTokens") {
			invalid = true
		}
		if cached != nil && written != nil && !invalid {
			r["ordinaryInputTokens"] = val(r, "inputTokens") - *cached - *written
			r["cacheKnown"] = true
		}
	} else if embedding && convention != "anthropic" && !invalid {
		r["cacheKnown"] = true
		r["ordinaryInputTokens"] = r["inputTokens"]
		r["cacheReadInputTokens"] = float64(0)
		r["cacheWriteInputTokens"] = float64(0)
	} else if convention == "anthropic" {
		invalid = true
	}
	sum := val(r, "inputTokens") + val(r, "outputTokens")
	if validCount(sum) {
		r["totalTokens"] = sum
	}
	if !validCount(val(r, "inputTokens")) || !validCount(sum) || total != nil && *total != sum {
		invalid = true
	}
	if !validCount(val(r, "inputTokens")) {
		r["inputTokens"] = float64(0)
	}
	if invalid {
		r["known"] = false
		if _, ok := r["cacheKnown"]; ok {
			r["cacheKnown"] = false
		}
		delete(r, "ordinaryInputTokens")
	}
	return r
}
func NormalizedUsage(value any) (Object, error) {
	u, e := obj(value)
	if e != nil {
		return nil, e
	}
	bad := func() (Object, error) { return nil, fail("invalid_provider_usage") }
	for _, k := range []string{"known", "cacheKnown"} {
		if v, ok := u[k]; ok {
			if _, ok := v.(bool); !ok {
				return bad()
			}
		}
	}
	r := Object{}
	for _, k := range []string{"inputTokens", "outputTokens", "totalTokens"} {
		n, ok := count(u[k])
		if !ok {
			return bad()
		}
		r[k] = n
	}
	if u["known"] == false {
		r["known"] = false
	}
	if v, ok := u["cacheKnown"]; ok {
		r["cacheKnown"] = v
	}
	sum := val(r, "inputTokens") + val(r, "outputTokens")
	if !validCount(sum) || sum != val(r, "totalTokens") {
		return bad()
	}
	for _, k := range cacheKeys {
		if v, ok := u[k]; ok {
			n, ok := count(v)
			if !ok {
				return bad()
			}
			r[k] = n
		}
	}
	_, hasRead := r["cacheReadInputTokens"]
	_, hasWrite := r["cacheWriteInputTokens"]
	typed := float64(0)
	for _, k := range cacheKeys[3:] {
		typed += val(r, k)
	}
	if !validCount(typed) || hasWrite && typed > val(r, "cacheWriteInputTokens") || val(r, "cacheReadInputTokens")+val(r, "cacheWriteInputTokens") > val(r, "inputTokens") {
		return bad()
	}
	if r["cacheKnown"] == true {
		if !hasRead || !hasWrite {
			return bad()
		}
		if _, ok := r["ordinaryInputTokens"]; !ok {
			r["ordinaryInputTokens"] = val(r, "inputTokens") - val(r, "cacheReadInputTokens") - val(r, "cacheWriteInputTokens")
		}
	}
	if _, ok := r["ordinaryInputTokens"]; ok && hasRead && hasWrite && val(r, "ordinaryInputTokens")+val(r, "cacheReadInputTokens")+val(r, "cacheWriteInputTokens") != val(r, "inputTokens") {
		return bad()
	}
	if v, ok := u["rawTokenCounters"]; ok {
		raw, e := obj(v)
		if e != nil {
			return nil, e
		}
		allow := map[string]bool{}
		for _, p := range rawPaths {
			allow[p] = true
		}
		out := Object{}
		for k, v := range raw {
			n, ok := count(v)
			if !allow[k] || !ok {
				return bad()
			}
			out[k] = n
		}
		r["rawTokenCounters"] = out
	}
	return r, nil
}
