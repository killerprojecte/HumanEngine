package httpapi

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"net/http"
	"strings"
)

// Delivery status is a separate, not-yet-migrated sink-owner contract. Do not
// silently manufacture its bounded per-sink counters from the native writer.
func retainedLogsPath(path string) bool {
	return (path == "/api/logs" || strings.HasPrefix(path, "/api/logs/")) && path != "/api/logs/status" && !strings.HasPrefix(path, "/api/logs/status/")
}

// WithRetainedLogs binds the server's actual logger to the same database and
// authority as every other application read. No diagnostic projection stands in
// for the producer-owned retention buffer.
func (s *Server) WithRetainedLogs(publisher *recovery.ReadPublisher) error {
	if s == nil || s.logs == nil || !publisher.OwnedBy(s.main, s.authority) {
		return errors.New("http_retained_logs_dependencies_invalid")
	}
	s.retainedPublisher = publisher
	return nil
}

func (s *Server) serveRetainedLogs(w http.ResponseWriter, r *http.Request, available bool, id string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	fail := func() {
		_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "logs.failed", Message: "Retained log read failed", RequestID: id})
		if !response.accepted && r.Context().Err() == nil {
			if r.Method == "HEAD" {
				w.WriteHeader(503)
			} else {
				habitError(w, 503, "Request failed; inspect server diagnostics", id)
			}
		}
	}
	if !available {
		fail()
		return
	}
	err := s.retainedPublisher.Publish(r.Context(), func(*sql.Conn) ([]byte, error) {
		if habitRoutingPath(r) != "/api/logs" || (r.Method != "GET" && r.Method != "HEAD") {
			response.status = 404
			response.header.Set("Content-Type", "text/plain; charset=UTF-8")
			return []byte("404 Not Found"), nil
		}
		return s.logs.RetainedJSON(), nil
	}, func(body []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return response.enqueue(r.Context(), body)
	})
	if response.accepted {
		err = errors.Join(err, response.drain(r.Context(), w))
	}
	if err != nil {
		fail()
	}
}
