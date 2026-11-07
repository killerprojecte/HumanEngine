package modelservice

import "github.com/killerprojecte/HumanEngine/backend/internal/plugins"

// V1ProtocolFailure is the trusted adapter from a typed V1 call failure to the
// model attempt policy. Only a direct worker-authenticated SDK failure is
// classified. It does not inspect arbitrary error text, JSON, errors.As or cause
// chains, and does not turn arbitrary plugin errors into native network errors.
// Callers still own cancellation/lease precedence, validation and continuation
// lifetimes. This helper alone is not a production provider invocation adapter.
func V1ProtocolFailure(err error) (*ModelError, bool) {
	failure, ok := plugins.ClassifyProtocolFailure(err)
	if !ok {
		return nil, false
	}
	result := withCause(NewError(failure.Code, failure.Status, failure.Retryable), err)
	result.RetryAfterMS = copyNumber(failure.RetryAfterMS)
	return result, true
}
