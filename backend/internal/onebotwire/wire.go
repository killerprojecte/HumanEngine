package onebotwire

import (
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/outboundmedia"
	"golang.org/x/text/encoding/unicode"
)

type Error struct {
	Code        string
	Transmitted bool
	HTTPStatus  int
}

func (e *Error) Error() string { return e.Code }
func Failure(code string, transmitted bool) error {
	return &Error{Code: code, Transmitted: transmitted}
}

// Preparation is native IO state only. Guest code has already selected every
// source and built the protocol envelope. Validation remains ordered by source.
type Preparation struct {
	Encoder *outboundmedia.Encoder
	Sources []outboundmedia.Source
}

// Encoder errors are fixed source codes; never expose arbitrary resolver errors.
func EncoderFailure(err error, timeout bool) error {
	code := err.Error()
	if code == "request_cancelled" && timeout {
		code = "request_timeout"
	}
	return Failure(code, false)
}

// Buffer.toString('utf8') replaces malformed bytes but preserves a leading BOM.
func BufferText(raw []byte) string {
	decoded, _ := unicode.UTF8.NewDecoder().Bytes(raw)
	return string(decoded)
}
func HeaderByteString(value string) (string, error) {
	units, err := jsjson.UTF16(value)
	if err != nil {
		return "", err
	}
	bytes := make([]byte, len(units))
	for i, unit := range units {
		if unit > 255 {
			return "", errors.New("invalid_header_value")
		}
		bytes[i] = byte(unit)
	}
	return string(bytes), nil
}
