//go:build web_read_candidate

package webread

import (
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"strings"
	"unicode/utf16"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	whatwg "github.com/nlnwa/whatwg-url/url"
)

// NetworkState is guest-owned policy. The host carries it opaquely between I/O
// observations and never decides public-address, redirect or header policy.
type NetworkState struct {
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	HeaderOrder  []string          `json:"headerOrder"`
	Body         *string           `json:"body,omitempty"` // base64-encoded wire bytes
	Follow       bool              `json:"follow"`
	MaxRedirects int64             `json:"maxRedirects"`
	Hop          int64             `json:"hop"`
}
type NetworkAddress struct {
	Address string `json:"address"`
	Family  int    `json:"family"`
}

var reservedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2001:2::/48"), netip.MustParsePrefix("2001:10::/28"), netip.MustParsePrefix("2001:20::/28"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func publicAddress(value string) bool {
	ip, e := netip.ParseAddr(value)
	if e != nil {
		return false
	}
	if ip.Is4() {
		if ip.String() == "168.63.129.16" {
			return false
		}
	} else if !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range reservedNetworks {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func networkError(message string, typeError bool) any {
	return map[string]any{"error": message, "typeError": typeError}
}
func validateURL(u *whatwg.Url) string {
	if !one(u.Protocol(), "http:", "https:") || u.Username() != "" || u.Password() != "" || u.Hash() != "" || len(utf16.Encode([]rune(u.Href(false)))) > 16384 {
		return "media_url_not_supported"
	}
	return ""
}
func headerToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}
func headerValue(s string) bool {
	for _, r := range s {
		if !(r == '\t' || r >= 32 && r <= 255 && r != 127) {
			return false
		}
	}
	return true
}
func networkPrepare(in map[string]any, rawInput []byte) any {
	args := obj(in["request"])
	method := strings.ToUpper(str(args["method"]))
	if method == "" {
		method = "GET"
	}
	_, hasBody := args["body"]
	timeout := number(in["timeoutMs"], 60000)
	max := number(args["maxRedirects"], 10)
	if !one(method, "GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE") || hasBody && method == "HEAD" || !integer(timeout, 1, 2147483647) || !integer(max, 0, 9007199254740991) {
		return networkError("invalid_network_request", false)
	}
	headers := map[string]string{}
	var order []string
	var raw struct {
		Request struct {
			Headers json.RawMessage `json:"headers"`
		} `json:"request"`
	}
	json.Unmarshal(rawInput, &raw)
	if len(raw.Request.Headers) > 0 {
		value, e := jsjson.Parse(string(raw.Request.Headers))
		if e != nil {
			return networkError("invalid_network_header", false)
		}
		if h, ok := value.(*jsjson.Object); ok {
			for _, entry := range h.Members() {
				k := entry.Key
				lower := strings.ToLower(k)
				value := str(entry.Value)
				if !headerToken(k) || !headerValue(value) || one(lower, "host", "connection", "proxy-connection", "proxy-authorization", "proxy-authenticate", "transfer-encoding", "content-length", "upgrade", "te", "trailer", "expect") {
					return networkError("invalid_network_header", false)
				}
				if _, exists := headers[lower]; !exists {
					order = append(order, lower)
				}
				headers[lower] = value
			}
		}
	}

	u, e := whatwg.Parse(str(args["url"]))
	if e != nil {
		return networkError("Invalid URL", true)
	}
	u.SetHash("")
	if code := validateURL(u); code != "" {
		return networkError(code, false)
	}
	state := NetworkState{URL: u.Href(false), Method: method, Headers: headers, HeaderOrder: order, Follow: args["followRedirects"] != false, MaxRedirects: int64(max)}
	if hasBody {
		body := str(args["body"])
		if args["bodyEncoding"] != "base64" {
			body = base64.StdEncoding.EncodeToString([]byte(body))
		}
		state.Body = &body
	}
	return networkHop(state)
}
func networkHop(state NetworkState) any {
	u, e := whatwg.Parse(state.URL)
	if e != nil {
		return networkError("invalid_media_reference", false)
	}
	hostname := strings.TrimSuffix(strings.TrimPrefix(u.Hostname(), "["), "]")
	port := u.Port()
	if port == "" {
		if u.Protocol() == "https:" {
			port = "443"
		} else {
			port = "80"
		}
	}
	// Spread overwrites a value without moving its original property slot.
	wireHeaders := [][2]string{{"accept-encoding", "identity"}}
	if value, ok := state.Headers["accept-encoding"]; ok {
		wireHeaders[0][1] = value
	}
	for _, name := range state.HeaderOrder {
		if name != "accept-encoding" {
			if value, ok := state.Headers[name]; ok {
				wireHeaders = append(wireHeaders, [2]string{name, value})
			}
		}
	}
	out := map[string]any{"wireHeaders": wireHeaders, "state": state, "hostname": hostname, "port": port, "tls": u.Protocol() == "https:", "host": u.Host(), "path": u.Pathname() + u.Search(), "dnsTimeoutMs": 5000}
	if ip, e := netip.ParseAddr(hostname); e == nil {
		family := 6
		if ip.Is4() {
			family = 4
		}
		out["literal"] = NetworkAddress{hostname, family}
	}
	return out
}
func networkChoose(in map[string]any) any {
	raw, _ := json.Marshal(in["addresses"])
	var addresses []NetworkAddress
	json.Unmarshal(raw, &addresses)
	if in["cancelled"] == true {
		return networkError("media_cancelled", false)
	}
	if len(addresses) == 0 {
		return networkError("media_address_not_public", false)
	}
	for _, a := range addresses {
		if !publicAddress(a.Address) {
			return networkError("media_address_not_public", false)
		}
	}
	for _, a := range addresses {
		if a.Family == 4 {
			return map[string]any{"address": a}
		}
	}
	return map[string]any{"address": addresses[0]}
}
func networkRedirect(in map[string]any) any {
	raw, _ := json.Marshal(in["state"])
	var state NetworkState
	json.Unmarshal(raw, &state)
	status := number(in["httpStatus"], 0)
	location := str(obj(in["headers"])["location"])
	if !state.Follow || !oneStatus(status, 301, 302, 303, 307, 308) || location == "" {
		return map[string]any{"response": true, "sourceUrl": state.URL}
	}
	if state.Hop >= state.MaxRedirects {
		return networkError("network_redirect_limit", false)
	}
	old, e := whatwg.Parse(state.URL)
	if e != nil {
		return networkError("Invalid URL", true)
	}
	next, e := old.Parse(location)
	if e != nil {
		return networkError("Invalid URL", true)
	}
	next.SetHash("")
	if code := validateURL(next); code != "" {
		return networkError(code, false)
	}
	if old.Protocol() == "https:" && next.Protocol() != "https:" {
		return networkError("network_redirect_downgrade", false)
	}
	if old.Protocol() != next.Protocol() || old.Host() != next.Host() {
		state.Headers = map[string]string{}
		state.HeaderOrder = nil
	}
	if status == 303 && state.Method != "HEAD" || (status == 301 || status == 302) && state.Method == "POST" {
		state.Method = "GET"
		state.Body = nil
		delete(state.Headers, "content-type")
		delete(state.Headers, "content-encoding")
	}
	state.URL = next.Href(false)
	state.Hop++
	return networkHop(state)
}
func oneStatus(s float64, values ...float64) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func networkInvoke(input []byte, op string) any {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return networkError("invalid_plugin_input", false)
	}
	switch op {
	case "networkHeaders":
		return networkHeaders(in)
	case "networkPrepare":
		return networkPrepare(in, input)
	case "networkChoose":
		return networkChoose(in)
	case "networkRedirect":
		return networkRedirect(in)
	}
	return networkError("unknown_plugin_operation", false)
}

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
