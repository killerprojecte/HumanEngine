//go:build web_read_candidate || web_read_public_get

package webread

import (
	"encoding/json"
	"strings"
)

func networkHeaders(in map[string]any) any {
	raw, _ := json.Marshal(in["rawHeaders"])
	var values []string
	json.Unmarshal(raw, &values)
	// Node 24's default HTTP parser exports at most 2000 alternating header
	// name/value entries. This source limit is covered by the TCP oracle; all
	// fields still participate in native framing validation before exposure.
	if len(values) > 2000 {
		values = values[:2000]
	}
	headers := map[string]any{}
	for i := 0; i+1 < len(values); i += 2 {
		name := strings.ToLower(values[i])
		value := values[i+1]
		previous, exists := headers[name]
		if name == "set-cookie" {
			if !exists {
				headers[name] = []string{value}
			} else {
				headers[name] = append(previous.([]string), value)
			}
			continue
		}
		if !exists {
			headers[name] = value
			continue
		}
		if one(name, "age", "authorization", "content-length", "content-type", "etag", "expires", "from", "host", "if-modified-since", "if-unmodified-since", "last-modified", "location", "max-forwards", "proxy-authorization", "referer", "retry-after", "server", "user-agent") {
			continue
		}
		separator := ", "
		if name == "cookie" {
			separator = "; "
		}
		headers[name] = str(previous) + separator + value
	}
	contentType := ""
	if value, ok := headers["content-type"]; ok {
		contentType = str(value)
	}
	return map[string]any{"headers": headers, "contentType": contentType, "rawHeaders": values}
}
