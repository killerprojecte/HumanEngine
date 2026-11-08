// Package pluginmanager owns plugin management state and safe public failures.
package pluginmanager

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
)

type Issue struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}
type ConfigurationError struct {
	RequestID string  `json:"requestId,omitempty"`
	Issues    []Issue `json:"issues"`
}

func (*ConfigurationError) Error() string { return "invalid_plugin_configuration" }

type NetworkDetails struct {
	SystemCode string `json:"systemCode,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
}
type StartupError struct {
	Code      string `json:"error"`
	RequestID string `json:"requestId,omitempty"`
	NetworkDetails
}

func (e *StartupError) Error() string { return e.Code }
func NewStartupError(code string, details map[string]any) *StartupError {
	if !startupCodes[code] {
		code = "plugin_setup_failed"
	}
	return &StartupError{Code: code, NetworkDetails: SafeNetworkDetails(details)}
}
func SafeNetworkDetails(value map[string]any) NetworkDetails {
	var result NetworkDetails
	code := value["systemCode"]
	if code == nil {
		code = value["code"]
	}
	if code == nil {
		if cause, ok := value["cause"].(map[string]any); ok {
			code = cause["code"]
		}
	}
	if code, ok := code.(string); ok && systemCodes[code] {
		result.SystemCode = code
	}
	if status, ok := value["httpStatus"].(float64); ok && status >= 100 && status <= 599 && math.Trunc(status) == status {
		result.HTTPStatus = int(status)
	}
	return result
}

var startupCodes = map[string]bool{
	"secret_key_unavailable":           true,
	"secret_key_missing":               true,
	"secret_key_permissions":           true,
	"secret_key_invalid":               true,
	"secret_decryption_failed":         true,
	"plugin_setup_failed":              true,
	"plugin_disabled":                  true,
	"invalid_plugin_configuration":     true,
	"credential_not_configured":        true,
	"event_credential_not_configured":  true,
	"account_mismatch":                 true,
	"connection_failed":                true,
	"connection_closed":                true,
	"request_timeout":                  true,
	"request_cancelled":                true,
	"adapter_not_connected":            true,
	"adapter_disposed":                 true,
	"invalid_websocket_url":            true,
	"invalid_http_url":                 true,
	"invalid_http_event_configuration": true,
	"http_event_listen_failed":         true,
	"http_action_failed":               true,
	"http_request_failed":              true,
	"invalid_http_response":            true,
	"http_response_too_large":          true,
	"invalid_payload":                  true,
	"invalid_identifier":               true,
	"unsupported":                      true,
	"invalid_action_parameters":        true,
	"platform_action_failed":           true,
}
var systemCodes = map[string]bool{
	"ECONNREFUSED":                    true,
	"ECONNRESET":                      true,
	"ENOTFOUND":                       true,
	"EAI_AGAIN":                       true,
	"EHOSTUNREACH":                    true,
	"ENETUNREACH":                     true,
	"ETIMEDOUT":                       true,
	"EADDRINUSE":                      true,
	"EADDRNOTAVAIL":                   true,
	"EACCES":                          true,
	"EPERM":                           true,
	"EPIPE":                           true,
	"CERT_HAS_EXPIRED":                true,
	"DEPTH_ZERO_SELF_SIGNED_CERT":     true,
	"ERR_TLS_CERT_ALTNAME_INVALID":    true,
	"UNABLE_TO_VERIFY_LEAF_SIGNATURE": true,
	"SELF_SIGNED_CERT_IN_CHAIN":       true,
	"UND_ERR_CONNECT_TIMEOUT":         true,
}

// PublicIssues is the source SnowLuma diagnostic filter and existing 32-issue
// response limit, never arbitrary submitted field names or values.
func PublicIssues(issues []Issue) []any {
	fields := map[string]bool{"configuration": true, "transport": true, "url": true, "accountId": true, "token": true, "clearToken": true, "credentialEnv": true, "eventToken": true, "clearEventToken": true, "eventHost": true, "eventPort": true, "eventPath": true, "eventCredentialEnv": true}
	reasons := map[string]bool{"required": true, "invalid_type": true, "invalid_value": true, "invalid_url": true, "websocket_scheme_required": true, "http_scheme_required": true, "url_credentials_forbidden": true, "url_query_forbidden": true, "url_fragment_forbidden": true, "invalid_event_path": true, "invalid_port": true, "conflicting_credential_change": true}
	result := []any{}
	for _, issue := range issues {
		if fields[issue.Field] && reasons[issue.Reason] {
			object, _ := jsjson.NewObject(jsjson.Member{Key: "field", Value: issue.Field}, jsjson.Member{Key: "reason", Value: issue.Reason})
			result = append(result, object)
			if len(result) == 32 {
				break
			}
		}
	}
	return result
}
