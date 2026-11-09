package snowlumatransport

import (
	"github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/outboundmedia"
	"io"
	"sync"
)

// Preparation is native IO state only. Guest code has already selected every
// source and built the protocol envelope. Validation remains ordered by source.
type Preparation struct {
	Encoder *outboundmedia.Encoder
	Sources []outboundmedia.Source
}

// heldBody releases the physical owner only when net/http closes the request
// body. Returning from Client.Do is not proof that its asynchronous write ended.
type heldBody struct {
	io.Reader
	hold io.Closer
	done func()
	once sync.Once
}

func (b *heldBody) Close() error {
	b.once.Do(func() { _ = b.hold.Close(); b.done() })
	return nil
}

// Encoder errors are fixed source codes; never expose arbitrary resolver errors.
func encoderFailure(err error, timeout bool) error {
	code := err.Error()
	if code == "request_cancelled" && timeout {
		code = "request_timeout"
	}
	return failure(code, false)
}
