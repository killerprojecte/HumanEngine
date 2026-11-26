package snowlumatransport

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/outboundmedia"
)

// Preparation is native IO state only. Guest code has already selected every
// source and built the protocol envelope. Validation remains ordered by source.
type Preparation struct {
	Encoder *outboundmedia.Encoder
	Sources []outboundmedia.Source
}

// Encoder errors are fixed source codes; never expose arbitrary resolver errors.
func encoderFailure(err error, timeout bool) error {
	code := err.Error()
	if code == "request_cancelled" && timeout {
		code = "request_timeout"
	}
	return failure(code, false)
}
