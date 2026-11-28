package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// publicationResponse is the request's private, one-entry output queue. Only
// its handler goroutine owns it; it never retains a database capability or a
// transport, and does not spawn another goroutine. Enqueue copies borrowed
// bytes or takes exclusive ownership of a fresh buffer. Draining is solely
// transport work: later revocation cannot change or revalidate this response.
// Like Node's end(body), acceptance does not promise delivery to the client.
// There is no body limit, admission limit, timeout, or shared output queue.
type publicationResponse struct {
	header   http.Header
	status   int
	head     bool
	body     string
	owned    []byte
	accepted bool
	consumed bool
	started  bool
}

func newPublicationResponse(header http.Header, head bool) *publicationResponse {
	return &publicationResponse{header: header.Clone(), head: head, status: http.StatusOK}
}

func (p *publicationResponse) checkEnqueue(ctx context.Context) error {
	if p.accepted || p.consumed {
		return errors.New("http_publication_already_accepted")
	}
	return ctx.Err()
}

func (p *publicationResponse) enqueue(ctx context.Context, body []byte) error {
	if err := p.checkEnqueue(ctx); err != nil {
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

// enqueueOwnedBytes takes a private, immutable response buffer without copying.
// The producer must not access the buffer after successful acceptance.
func (p *publicationResponse) enqueueOwnedBytes(ctx context.Context, body []byte) error {
	if err := p.checkEnqueue(ctx); err != nil {
		return err
	}
	if !p.head {
		p.owned = body
	}
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
	body, owned := p.body, p.owned
	p.body, p.owned = "", nil
	if err := ctx.Err(); err != nil {
		return err
	}
	header := w.Header()
	for key, values := range p.header {
		header[key] = values
	}
	p.header = nil
	p.started = true
	status := p.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if p.head {
		return nil
	}
	var n, length int
	var err error
	if owned != nil {
		length = len(owned)
		n, err = w.Write(owned)
	} else {
		length = len(body)
		n, err = io.WriteString(w, body)
	}
	if err == nil && n != length {
		return io.ErrShortWrite
	}
	return err
}

func (s *Server) publishResponse(ctx context.Context, path string, response *publicationResponse) error {
	return s.publishProjection(ctx, func(conn *sql.Conn) ([]byte, error) {
		return s.projection(ctx, conn, path)
	}, func(body []byte) error {
		return response.enqueue(ctx, body)
	})
}

// WithProjectionPublisher moves readonly admin projections off the single
// writer connection. Standalone read-only servers keep their owned reader.
func (s *Server) WithProjectionPublisher(p *recovery.ReadPublisher) error {
	if !p.OwnedBy(s.main, s.authority) {
		return errors.New("http_projection_publisher_invalid")
	}
	s.projectionPublisher = p
	return nil
}

func (s *Server) publishProjection(ctx context.Context, read func(*sql.Conn) ([]byte, error), emit func([]byte) error) error {
	p := s.projectionPublisher
	if p == nil {
		return s.authority.Publish(ctx, s.main, read, emit)
	}
	err := p.Publish(ctx, read, emit)
	if err != nil && p.AwaitLocalIntent(ctx, err) {
		// Nothing was enqueued; re-read after the matching local write commits.
		return p.Publish(ctx, read, emit)
	}
	return err
}
