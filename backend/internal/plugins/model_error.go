package plugins

import (
	"fmt"
	"strconv"
)

// protocolFailure is private control-plane metadata, never plugin return data.
// Retry decisions were evaluated in the owning worker using the canonical SDK
// constructor and the source invokeProtocol branch before crossing JSON.
type protocolFailure struct {
	Code         string  `json:"code"`
	Status       *int    `json:"status,omitempty"`
	Retryable    bool    `json:"retryable"`
	RetryAfterMS *string `json:"retryAfterMs,omitempty"`
}

type nodeProtocolFailure struct{ protocol *protocolFailure }

func (*nodeProtocolFailure) Error() string { return "model_protocol_failure" }
func nodeReplyError(reply nodeReply) error {
	if reply.ProtocolFailure != nil {
		return &nodeProtocolFailure{protocol: reply.ProtocolFailure}
	}
	return fmt.Errorf("V1 plugin: %s", reply.Error)
}

// ProtocolFailureClassification is a copied, sanitized policy projection, not
// an error constructor. It cannot confer identity on a Go error or JSON value.
type ProtocolFailureClassification struct {
	Code         string
	Status       *int
	Retryable    bool
	RetryAfterMS *float64
}

// ClassifyProtocolFailure accepts only a direct transport-created invocation
// failure. Name/shape matches, wrapped errors, errors.As hooks, zero values and
// error JSON roundtrips never confer retry eligibility. The V1 host is a trusted
// plugin lifecycle boundary, not a security sandbox for arbitrary Node code.
func ClassifyProtocolFailure(err error) (ProtocolFailureClassification, bool) {
	invocation, ok := err.(*ProtocolInvocationError)
	if !ok || invocation == nil || invocation.protocol == nil {
		return ProtocolFailureClassification{}, false
	}
	failure := invocation.protocol
	result := ProtocolFailureClassification{Code: failure.Code, Retryable: failure.Retryable}
	if failure.Status != nil {
		value := *failure.Status
		result.Status = &value
	}
	if failure.RetryAfterMS != nil {
		if value, err := strconv.ParseFloat(*failure.RetryAfterMS, 64); err == nil {
			result.RetryAfterMS = &value
		}
	}
	return result, true
}
