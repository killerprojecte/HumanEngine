package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

const memoryDeletePrefix = "/api/memories"

func memoryDeletePath(path string) bool {
	return path == memoryDeletePrefix || strings.HasPrefix(path, memoryDeletePrefix+"/")
}

// Keep the other real source endpoints at their explicit preview boundary.
// Unknown methods/paths in this family are source 404s, not new CRUD handlers.
func memoryDeleteUnmigrated(r *http.Request) bool {
	path := habitRoutingPath(r)
	if path == memoryDeletePrefix {
		return r.Method == "GET" || r.Method == "HEAD" || r.Method == "POST"
	}
	if path == memoryDeletePrefix+"/page" && (r.Method == "GET" || r.Method == "HEAD") {
		return true
	}
	tail := strings.TrimPrefix(path, memoryDeletePrefix+"/")
	return r.Method == "PATCH" && tail != "" && !strings.Contains(tail, "/")
}

func memoryDeleteRoute(r *http.Request) (string, bool) {
	path := habitRoutingPath(r)
	if !strings.HasPrefix(path, memoryDeletePrefix+"/") || r.Method != "DELETE" {
		return "", false
	}
	tail := strings.TrimPrefix(path, memoryDeletePrefix+"/")
	if tail == "" || strings.Contains(tail, "/") {
		return "", false
	}
	// Hono decodes the matched parameter separately, preserving encoded slashes
	// as one ID and malformed escapes as literal strings. params is z.string(),
	// with no length cap, scene, body, query or model authorization requirement.
	return habitTryDecode(tail, false), true
}

func (s *Server) serveMemoryDelete(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	report := func(e error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "memory.delete.failed", Message: e.Error(), RequestID: requestID})
		}
	}
	failure := func(e error) {
		report(e)
		if r.Method == "HEAD" {
			w.WriteHeader(503)
		} else {
			habitError(w, 503, "Request failed; inspect server diagnostics", requestID)
		}
	}
	if !available {
		failure(errors.New("memory_delete_request_unavailable"))
		return
	}
	// Source middleware checks readiness before running the route. Its early
	// failure has no cache headers, while synchronous lifecycle errors return
	// through the middleware and retain the normal authenticated headers.
	if err := s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil }); err != nil {
		failure(err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	response.header.Set("Cache-Control", "no-store")
	response.header.Set("X-Content-Type-Options", "nosniff")
	id, route := memoryDeleteRoute(r)
	var err error
	if !route {
		response.status = 404
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
		err = s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) {
			return []byte("404 Not Found"), nil
		}, func(body []byte) error { return response.enqueue(r.Context(), body) })
	} else {
		// No body parsing: source ignores even malformed JSON and content type.
		// The owned writer checks authenticated readiness before any lifecycle read.
		err = s.memoryDelete.PublishDelete(r.Context(), s.main, s.authority, id, func(body []byte) error {
			return response.enqueue(r.Context(), body)
		})
	}
	if response.accepted {
		err = errors.Join(err, response.drain(r.Context(), w))
	}
	if err != nil {
		if !response.accepted && r.Context().Err() == nil {
			failure(err)
		} else {
			report(err)
		}
	}
}
