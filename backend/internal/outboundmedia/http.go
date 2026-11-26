package outboundmedia

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
)

var ErrTransportStopped = errors.New("outbound_transport_stopped")

// BindRequest keeps the leased bytes alive until net/http closes its physical
// writer. Redirect and automatic replay callers cannot borrow an unowned body.
func (l *Lease) BindRequest(request *http.Request, work *quiescence.Group) error {
	hold, err := l.Hold()
	if err != nil {
		return err
	}
	done, ok := work.Child()
	if !ok {
		_ = hold.Close()
		return ErrTransportStopped
	}
	request.Body = &heldBody{Reader: bytes.NewReader(l.Bytes), hold: hold, done: done}
	request.ContentLength = int64(len(l.Bytes))
	request.GetBody = nil
	return nil
}

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
