package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
)

// publicationResponse is the request's private, one-entry output queue. Only
// its handler goroutine owns it; it never retains a database capability or a
// transport, and does not spawn another goroutine. Enqueue transfers an immutable
// copy of the complete body under the authority reservation. Draining is solely
// transport work: later revocation cannot change or revalidate this response.
// Like Node's end(body), acceptance does not promise delivery to the client.
// There is no body limit, admission limit, timeout, or shared output queue.
type publicationResponse struct {
	header   http.Header
	head     bool
	body     string
	accepted bool
	consumed bool
	started  bool
}

func newPublicationResponse(header http.Header, head bool) *publicationResponse {
	return &publicationResponse{header: header.Clone(), head: head}
}

func (p *publicationResponse) enqueue(ctx context.Context, body []byte) error {
	if p.accepted || p.consumed {
		return errors.New("http_publication_already_accepted")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A string conversion owns the bytes, including if the producer reuses its
	// slice. HEAD validates the same projection but has no body to enqueue.
	var owned string
	if !p.head {
		owned = string(body)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.body = owned
	p.accepted = true
	return nil
}

func (p *publicationResponse) drain(ctx context.Context, w http.ResponseWriter) error {
	if !p.accepted || p.consumed {
		return errors.New("http_publication_not_pending")
	}
	// Consume before any transport call or cancellation check: failed/partial
	// output must never be replayed, nor replaced with an error response.
	p.consumed = true
	body := p.body
	p.body = ""
	if err := ctx.Err(); err != nil {
		return err
	}
	header := w.Header()
	for key, values := range p.header {
		header[key] = values
	}
	p.header = nil
	p.started = true
	w.WriteHeader(http.StatusOK)
	if p.head {
		return nil
	}
	n, err := io.WriteString(w, body)
	if err == nil && n != len(body) {
		return io.ErrShortWrite
	}
	return err
}

func (s *Server) publishResponse(ctx context.Context, path string, response *publicationResponse) error {
	return s.authority.Publish(ctx, s.main, func(conn *sql.Conn) ([]byte, error) {
		return s.projection(ctx, conn, path)
	}, func(body []byte) error {
		return response.enqueue(ctx, body)
	})
}
