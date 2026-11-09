package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type logStatusOwner struct {
	store     *observability.StoreLogger
	publisher *recovery.ReadPublisher
}

// WithLogStatus binds only the source Store already used by this server's
// producers and retained reads. The raw Logger has a different status contract.
func (s *Server) WithLogStatus(publisher *recovery.ReadPublisher) error {
	if s == nil {
		return errors.New("http_log_status_dependencies_invalid")
	}
	store, ok := s.logs.(*observability.StoreLogger)
	if !ok || store == nil || !publisher.OwnedBy(s.main, s.authority) {
		return errors.New("http_log_status_dependencies_invalid")
	}
	s.logStatus = &logStatusOwner{store: store, publisher: publisher}
	return nil
}

func logStatusPath(path string) bool {
	return path == "/api/logs/status" || strings.HasPrefix(path, "/api/logs/status/")
}

// Source Hono catches a throwing getter. Do not expose panic values, paths or
// runtime errors, and never invoke the side-effecting getter again on failure.
func sourceLogStatus(store *observability.StoreLogger) (body []byte, err error) {
	defer func() {
		if recover() != nil {
			body = nil
			err = errors.New("http_log_status_getter_failed")
		}
	}()
	return json.Marshal(store.LoggingStatus())
}

func (s *Server) serveLogStatus(w http.ResponseWriter, r *http.Request, available bool, id string) {
	response := newPublicationResponse(w.Header(), r.Method == http.MethodHead)
	started := time.Now()
	var handlerStarted, handlerFinished time.Time
	handlerFailed := false
	setHandlerHeaders := func(header http.Header) {
		finished := time.Now()
		header.Set("Cache-Control", "no-store")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Server-Timing", fmt.Sprintf("recovery;dur=%.2f, handler;dur=%.2f, total;dur=%.2f",
			float64(handlerStarted.Sub(started)+finished.Sub(handlerFinished))/float64(time.Millisecond),
			float64(handlerFinished.Sub(handlerStarted))/float64(time.Millisecond),
			float64(finished.Sub(started))/float64(time.Millisecond)))
	}
	report := func() {
		// Runs after Publish has released its database snapshots/authority lock.
		// The exact request ID is shared by the response and sanitized diagnostic.
		_ = s.logs.Emit(observability.Event{Level: "error", Module: "api", Code: "api_unhandled_error", RequestID: id, Fields: map[string]any{"operation": r.Method + " /api/logs/status", "phase": "request"}})
	}
	failure := func() {
		report()
		if response.accepted || r.Context().Err() != nil {
			return
		}
		if handlerFailed {
			// A throwing route getter is handled inside Hono's next(), so the
			// authenticated middleware still adds these headers. Readiness or
			// later publication-fence failure does not reach that point.
			setHandlerHeaders(w.Header())
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			habitError(w, http.StatusServiceUnavailable, "Request failed; inspect server diagnostics", id)
		}
	}
	if !available {
		failure()
		return
	}
	owner := s.logStatus
	err := owner.publisher.Publish(r.Context(), func(*sql.Conn) ([]byte, error) {
		handlerStarted = time.Now()
		defer func() { handlerFinished = time.Now() }()
		if habitRoutingPath(r) != "/api/logs/status" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			response.status = http.StatusNotFound
			response.header.Set("Content-Type", "text/plain; charset=UTF-8")
			return []byte("404 Not Found"), nil
		}
		// This is the only status getter call. Publish invokes read once after
		// readiness/head validation, then validates and enqueues the same body.
		// A canceled/stale attempt retains any notice but is never retried here.
		body, err := sourceLogStatus(owner.store)
		handlerFailed = err != nil
		return body, err
	}, func(body []byte) error {
		setHandlerHeaders(response.header)
		return response.enqueue(r.Context(), body)
	})
	if response.accepted {
		// Transport happens after authority and database ownership is released.
		err = errors.Join(err, response.drain(r.Context(), w))
	}
	if err != nil {
		failure()
	}
}
