package snowluma

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	whatwg "github.com/nlnwa/whatwg-url/url"
	"math"
	"regexp"
	"strings"
)

var snowEnvironment = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
var snowEventPath = regexp.MustCompile(`^/[a-zA-Z0-9/_-]+$`)

func snowTrim(s string) string {
	return strings.Trim(s, "\u0009\u000a\u000b\u000c\u000d\u0020\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

// ValidateConfigurationDecision runs both source custom diagnostics and schema
// parsing in the guest. Numeric/string bounds below are the source schema's,
// not additional native admission policy. Unknown keys never enter diagnostics.
func ValidateConfigurationDecision(input *jsjson.Object) any {
	value, err := object(property(input, "config"))
	if err != nil {
		return httpJSON{"kind": "issues", "issues": []any{makeObject("field", "configuration", "reason", "invalid_type")}}
	}
	issues := []any{}
	seen := map[string]bool{}
	add := func(field, reason string) {
		issues = append(issues, makeObject("field", field, "reason", reason))
		seen[field] = true
	}
	schemaIssue := func(field, reason string) {
		if !seen[field] {
			add(field, reason)
		}
	}
	http := sameString(property(value, "transport"), "http")
	if s, ok := property(value, "accountId").(string); !ok || snowTrim(s) == "" {
		add("accountId", "required")
	}
	address, ok := property(value, "url").(string)
	if !ok || snowTrim(address) == "" {
		add("url", "required")
	} else {
		lower := strings.ToLower(address)
		scheme := "websocket_scheme_required"
		validPrefix := strings.HasPrefix(lower, "ws://") || strings.HasPrefix(lower, "wss://")
		if http {
			scheme = "http_scheme_required"
			validPrefix = strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
		}
		if !validPrefix {
			add("url", scheme)
		} else if u, e := whatwg.Parse(address); e != nil {
			add("url", "invalid_url")
		} else {
			if u.Hostname() == "" {
				add("url", "invalid_url")
			}
			if u.Username() != "" || u.Password() != "" {
				add("url", "url_credentials_forbidden")
			}
			if u.Search() != "" {
				add("url", "url_query_forbidden")
			}
			if u.Hash() != "" {
				add("url", "url_fragment_forbidden")
			}
		}
	}
	if truthy(property(value, "clearToken")) && truthy(property(value, "token")) {
		add("token", "conflicting_credential_change")
	}
	if http {
		if truthy(property(value, "clearEventToken")) && truthy(property(value, "eventToken")) {
			add("eventToken", "conflicting_credential_change")
		}
		if _, exists := value.Get("eventPort"); !exists {
			add("eventPort", "required")
		} else if port, ok := property(value, "eventPort").(float64); !ok || math.Trunc(port) != port || port < 1 || port > 65535 {
			add("eventPort", "invalid_port")
		}
		if path, exists := value.Get("eventPath"); exists {
			if s, ok := path.(string); !ok || !snowEventPath.MatchString(s) || strings.Contains(s, "//") {
				add("eventPath", "invalid_event_path")
			}
		}
	}
	raw := value.Clone()
	if !http && (sameString(property(raw, "transport"), "websocket") || isSnowMissing(raw, "transport")) {
		for _, key := range []string{"eventHost", "eventPort", "eventPath", "eventCredentialEnv", "eventToken", "clearEventToken"} {
			raw.Delete(key)
		}
	}
	parsed := new(jsjson.Object)
	aborted := false
	fields := []string{"transport", "url", "accountId", "token", "clearToken", "eventToken", "clearEventToken", "credentialEnv", "eventHost", "eventPort", "eventPath", "eventCredentialEnv"}
	known := map[string]bool{}
	for _, key := range fields {
		known[key] = true
		v, exists := raw.Get(key)
		if !exists {
			switch key {
			case "transport":
				v = "websocket"
			case "eventHost":
				v = "127.0.0.1"
			case "eventPath":
				v = "/onebot/events"
			case "url", "accountId":
				schemaIssue(key, "required")
				aborted = true
				continue
			default:
				continue
			}
		}
		valid := true
		switch key {
		case "transport", "eventHost":
			s, ok := v.(string)
			valid = ok
			if key == "transport" {
				valid = valid && (s == "websocket" || s == "http")
			} else {
				valid = valid && (s == "127.0.0.1" || s == "::1" || s == "0.0.0.0" || s == "::")
			}
			if !valid {
				schemaIssue(key, "invalid_value")
				aborted = true
			}
		case "clearToken", "clearEventToken":
			if _, ok := v.(bool); !ok {
				valid = false
				aborted = true
				schemaIssue(key, "invalid_type")
			}
		case "eventPort":
			n, ok := v.(float64)
			if !ok || math.Trunc(n) != n {
				valid = false
				aborted = true
				schemaIssue(key, "invalid_type")
			} else if n < 1 || n > 65535 {
				schemaIssue(key, "invalid_value")
			}
		default:
			s, ok := v.(string)
			if !ok {
				valid = false
				aborted = true
				schemaIssue(key, "invalid_type")
				break
			}
			length := snowCodePointLength(s)
			min, max := 1, 256
			switch key {
			case "url":
				max = 2048
			case "token", "eventToken":
				min = 0
				max = 16384
			case "credentialEnv", "eventCredentialEnv":
				max = 128
			}
			if length < min || length > max {
				schemaIssue(key, "invalid_value")
			}
			if (key == "token" || key == "eventToken") && s != "" && snowTrim(s) == "" {
				schemaIssue(key, "invalid_value")
			}
			if (key == "credentialEnv" || key == "eventCredentialEnv") && !snowEnvironment.MatchString(s) {
				schemaIssue(key, "invalid_value")
			}
			if key == "eventPath" && !snowEventPath.MatchString(s) {
				schemaIssue(key, "invalid_value")
			}
		}
		if valid {
			_ = parsed.Set(key, v)
		}
	}
	for _, key := range raw.Keys() {
		if !known[key] {
			schemaIssue("configuration", "invalid_value")
			aborted = true
			break
		}
	}
	if !aborted {
		if truthy(property(parsed, "clearToken")) && truthy(property(parsed, "token")) {
			schemaIssue("token", "invalid_value")
		}
		if truthy(property(parsed, "clearEventToken")) && truthy(property(parsed, "eventToken")) {
			schemaIssue("eventToken", "invalid_value")
		}
		u, e := whatwg.Parse(textProperty(parsed, "url"))
		if e != nil {
			schemaIssue("url", "invalid_value")
		} else {
			protocol := u.Protocol()
			good := protocol == "ws:" || protocol == "wss:"
			if sameString(property(parsed, "transport"), "http") {
				good = protocol == "http:" || protocol == "https:"
			}
			if !good || u.Username() != "" || u.Password() != "" || u.Search() != "" || u.Hash() != "" {
				schemaIssue("url", "invalid_value")
			}
			if strings.Contains(textProperty(parsed, "eventPath"), "//") {
				schemaIssue("eventPath", "invalid_value")
			}
			if sameString(property(parsed, "transport"), "http") && !truthy(property(parsed, "eventPort")) {
				schemaIssue("eventPort", "invalid_value")
			}
		}
	}
	if len(issues) > 0 {
		return httpJSON{"kind": "issues", "issues": issues}
	}
	return httpJSON{"kind": "configuration", "config": parsed}
}
func isSnowMissing(o *jsjson.Object, key string) bool { _, ok := o.Get(key); return !ok }

// The repository's Zod version counts code points for string min/max checks.
// Lone surrogates remain one unit; valid surrogate pairs count as one point.
func snowCodePointLength(s string) int {
	u, _ := jsjson.UTF16(s)
	n := 0
	for i := 0; i < len(u); i++ {
		n++
		if u[i] >= 0xd800 && u[i] <= 0xdbff && i+1 < len(u) && u[i+1] >= 0xdc00 && u[i+1] <= 0xdfff {
			i++
		}
	}
	return n
}
