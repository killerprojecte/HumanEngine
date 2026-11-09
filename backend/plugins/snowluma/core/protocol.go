// Package snowluma preserves OneBot identifiers and CQ parsing in the V2 guest.
package snowluma

import (
	"encoding/base64"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsoncompat"
	resources "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma"
	"strings"
)

type Segment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

func unescape(text string) string {
	for _, pair := range [][2]string{{"&#91;", "["}, {"&#93;", "]"}, {"&#44;", ","}, {"&amp;", "&"}} {
		text = strings.ReplaceAll(text, pair[0], pair[1])
	}
	return text
}
func ParseCQ(value string) []Segment {
	result := []Segment{}
	cursor, windowStart := 0, 0
	for windowStart < len(value) {
		relativeEnd := strings.IndexByte(value[windowStart:], ']')
		if relativeEnd < 0 {
			break
		}
		end := windowStart + relativeEnd
		window := value[windowStart:end]
		start := strings.Index(window, "[CQ:")
		for start >= 0 {
			typeStart := start + 4
			typeEnd := typeStart
			for typeEnd < len(window) {
				c := window[typeEnd]
				if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' {
					typeEnd++
				} else {
					break
				}
			}
			if typeEnd > typeStart && (typeEnd == len(window) || window[typeEnd] == ',') {
				absoluteStart := windowStart + start
				if absoluteStart > cursor {
					result = append(result, Segment{"text", map[string]any{"text": unescape(value[cursor:absoluteStart])}})
				}
				data := map[string]any{}
				for _, pair := range strings.Split(window[typeEnd:], ",")[1:] {
					equal := strings.IndexByte(pair, '=')
					if equal > 0 && pair[:equal] != "__proto__" {
						data[pair[:equal]] = unescape(pair[equal+1:])
					}
				}
				result = append(result, Segment{window[typeStart:typeEnd], data})
				cursor = end + 1
				break
			}
			next := strings.Index(window[typeEnd:], "[CQ:")
			if next < 0 {
				start = -1
			} else {
				start = typeEnd + next
			}
		}
		windowStart = end + 1
	}
	if cursor < len(value) {
		result = append(result, Segment{"text", map[string]any{"text": unescape(value[cursor:])}})
	}
	return result
}
func encoded(values ...string) string {
	return base64.RawURLEncoding.EncodeToString(jsoncompat.StringArray(values...))
}
func SceneID(adapter, account, kind, peer string) string {
	return "ob:s:" + encoded(adapter, account, kind, peer)
}
func MessageID(scene, platform string) string {
	scalar := jsoncompat.StringArray(platform)
	return scene + ":m:" + base64.RawURLEncoding.EncodeToString(scalar[1:len(scalar)-1])
}
func UserID(adapter, account, platform string) string {
	return "ob:u:" + encoded(adapter, account, platform)
}
func Invoke(input []byte) any {
	if parsed, err := jsjson.Parse(string(input)); err == nil {
		if object, ok := parsed.(*jsjson.Object); ok {
			operation, _ := object.Get("operation")
			if operation == "recalled_lookup" {
				return RecalledLookupDecision(object)
			}
			if operation == "history_begin" || operation == "message_begin" || operation == "message_recalled" || operation == "history_response" || operation == "history_normalized" || operation == "history_sorted" {
				return HistoryDecision(object)
			}
			if operation == "profiles_observe" || operation == "profiles_complete" || operation == "profiles_dispose" {
				return ProfileDecision(object)
			}
			if operation == "ingress_begin" || operation == "ingress_recalled" || operation == "ingress_callback" {
				return IngressDecision(object)
			}
			if operation == "configuration_secrets" || operation == "configuration_secret_present" {
				return ConfiguredSecretsDecision(object)
			}
			if operation == "configuration_validate" {
				return ValidateConfigurationDecision(object)
			}
			if operation == "configuration_begin" || operation == "configuration_applied" {
				return ConfigurationDecision(object)
			}
			if operation == "startup_begin" || operation == "startup_secret" || operation == "startup_environment" {
				return StartupDecision(object)
			}
			if operation == "engine_source_select" || operation == "engine_source_value" || operation == "engine_source_validate" {
				return OriginalDecision(object)
			}
			if operation == "send_validate" || operation == "send_begin" || operation == "send_source" || operation == "send_media" || operation == "send_result" {
				return SendDecision(object)
			}
			if operation == "receive" {
				return ReceiveDecision(object)
			}
			if operation == "normalize" || operation == "identify_message" {
				return NormalizationDecision(object)
			}
			if name, ok := operation.(string); ok && (name == "http_event" || name == "http_response" || name == "http_login") {
				return HTTPDecision(name, object)
			}
		}
	}

	var in struct{ Operation, Text, Adapter, Account, Kind, Peer, Scene, Platform string }
	if json.Unmarshal(input, &in) != nil {
		return map[string]any{"error": "invalid_plugin_input"}
	}
	switch in.Operation {
	case "manifest":
		return resources.Manifest()
	case "parse_cq":
		return ParseCQ(in.Text)
	case "identifiers":
		return map[string]any{"sceneId": SceneID(in.Adapter, in.Account, in.Kind, in.Peer), "messageId": MessageID(in.Scene, in.Platform), "userId": UserID(in.Adapter, in.Account, in.Platform)}
	default:
		return map[string]any{"error": "unknown_plugin_operation"}
	}
}
