package modelservice

import (
	"strconv"
	"strings"
	"sync/atomic"
)

// ModelError is a trusted core classification, not a JSON error shape. Merely
// wrapping one, matching its message, or exposing a status field grants no retry.
// Causes and retained failures never appear in its public text or JSON.
type ModelError struct {
	Code         string   `json:"code"`
	Status       *int     `json:"status,omitempty"`
	Retryable    bool     `json:"retryable"`
	RetryAfterMS *float64 `json:"retryAfterMs,omitempty"`
	cause        error
	failures     []error
	handled      atomic.Bool
}

func NewError(code string, status *int, retryable bool) *ModelError {
	e := &ModelError{Code: code, Retryable: retryable}
	if status != nil {
		v := *status
		e.Status = &v
	}
	return e
}
func (e *ModelError) Error() string     { return e.Code }
func (e *ModelError) Unwrap() error     { return e.cause }
func (e *ModelError) Failures() []error { return append([]error(nil), e.failures...) }
func withCause(e *ModelError, cause error) *ModelError {
	e.cause = cause
	if ProviderAttemptsHandled(cause) {
		e.handled.Store(true)
	}
	return e
}

type handledFailure struct{ error }

func (e *handledFailure) Unwrap() error { return e.error }

func ProviderAttemptsHandled(err error) bool {
	switch e := err.(type) {
	case *ModelError:
		return e.handled.Load()
	case *handledFailure:
		return true
	default:
		return false
	}
}
func markHandled(err error) error {
	if e, ok := err.(*ModelError); ok {
		e.handled.Store(true)
		return e
	}
	if ProviderAttemptsHandled(err) {
		return err
	}
	return &handledFailure{err}
}
func HTTPFailure(status int, retryAfterMS *float64) *ModelError {
	e := NewError("provider_http_"+strconv.Itoa(status), &status, status == 429 || status >= 500)
	e.RetryAfterMS = copyNumber(retryAfterMS)
	return e
}

// BuiltinNetworkFailure accepts only explicitly inspected transport error codes.
// Never call it on arbitrary V1 error text, wrapped errors, or untrusted JSON.
func BuiltinNetworkFailure(code, ownCauseCode string, cause error) *ModelError {
	transient := func(v string) bool {
		switch v {
		case "ECONNRESET", "ECONNREFUSED", "ETIMEDOUT", "EAI_AGAIN", "ENETUNREACH", "EHOSTUNREACH", "EPIPE", "UND_ERR_CONNECT_TIMEOUT", "UND_ERR_SOCKET":
			return true
		}
		return false
	}
	return withCause(NewError("provider_request_failed", nil, transient(code) || transient(ownCauseCode)), cause)
}
func IsReadAdmissionCode(code string) bool {
	return code == "memory_read_disabled" || code == "memory_read_revoked" || code == "personalization_disabled"
}

// Exact source allowlist: a made-up budget_* code must not silently acquire
// terminal admission semantics.
func IsBudgetFailureCode(code string) bool {
	switch code {
	case "budget_admission_failed", "budget_external_cost_unknown", "budget_restore_accounting_hold", "budget_amount_overflow", "budget_estimate_unknown", "budget_price_unknown", "budget_contract_overrun", "budget_daily_limit", "budget_lane_limit", "budget_turn_amount", "budget_turn_calls", "budget_turn_tokens", "budget_turn_identity_required", "budget_unbounded_exposure", "budget_unresolved_limit", "budget_output_cap_required", "budget_output_cap_unverified", "budget_protocol_unsupported", "budget_opaque_input_unsupported", "budget_verified_estimator_required", "budget_estimate_contract_mismatch", "budget_attempt_already_started", "budget_attempt_identity_conflict", "budget_idempotency_conflict", "budget_invalid_transition", "budget_attempt_owner", "budget_attempt_missing", "budget_release_started", "budget_receipt_conflict", "budget_configuration_invalid", "budget_configuration_changed":
		return true
	}
	return false
}
func StopsFallback(err error) bool {
	e, ok := err.(*ModelError)
	return ok && (e.Code == "protocol_disabled" || strings.HasPrefix(e.Code, "provider_stream_") || IsBudgetFailureCode(e.Code) || IsReadAdmissionCode(e.Code))
}
func cancelled(cause error) *ModelError {
	return withCause(NewError("request_cancelled", nil, false), cause)
}
