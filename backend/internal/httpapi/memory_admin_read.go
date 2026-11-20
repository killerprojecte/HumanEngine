package httpapi

import (
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func (s *Server) WithMemoryAdminRead(publisher *recovery.ReadPublisher) error {
	if s.memoryDelete == nil {
		return errors.New("http_memory_admin_read_owner_invalid")
	}
	owner, err := s.memoryDelete.BindAdminRead(publisher)
	if err != nil {
		return err
	}
	s.memoryAdminRead = owner
	return nil
}

func (s *Server) serveMemoryAdminRead(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	ctx := r.Context()
	head := r.Method == http.MethodHead
	response := newPublicationResponse(w.Header(), head)
	fail := func() {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "memory_admin_read.failed", Message: "Memory administration read failed", RequestID: requestID})
		}
		if !response.accepted && ctx.Err() == nil {
			if head {
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				habitError(w, http.StatusServiceUnavailable, "Request failed; inspect server diagnostics", requestID)
			}
		}
	}
	if !available {
		fail()
		return
	}
	readStarted, err := s.memoryAdminRead.PublishList(ctx, func(body []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return response.enqueue(ctx, body)
	})
	if response.accepted {
		err = errors.Join(err, response.drain(ctx, w))
	}
	if err != nil {
		// Source handler errors inside an authenticated ready read preserve
		// the normal response headers, even when the snapshot was rejected.
		if readStarted && !response.accepted {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		fail()
	}
}
