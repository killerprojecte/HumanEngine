package httpapi

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/usageview"
	"net/http"
	"strconv"
	"strings"
)

func usagePath(path string) bool {
	return path == "/api/usage" || strings.HasPrefix(path, "/api/usage/")
}

func (s *Server) WithUsage(store *usageview.Store, publisher *recovery.ReadPublisher) error {
	if !store.OwnedBy(s.main) || !publisher.OwnedBy(s.main, s.authority) {
		return errors.New("http_usage_owner_invalid")
	}
	s.usage = store
	s.usagePublisher = publisher
	return nil
}
func (s *Server) serveUsage(w http.ResponseWriter, r *http.Request, available bool, id string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	fail := func(err error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "usage.failed", Message: "Usage read failed", RequestID: id})
		}
		if !response.accepted && r.Context().Err() == nil {
			if r.Method == "HEAD" {
				w.WriteHeader(503)
			} else {
				habitError(w, 503, "Request failed; inspect server diagnostics", id)
			}
		}
	}
	if !available {
		fail(errors.New("request_unavailable"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	err := s.usagePublisher.Publish(r.Context(), func(conn *sql.Conn) ([]byte, error) {
		if habitRoutingPath(r) != "/api/usage" || (r.Method != "GET" && r.Method != "HEAD") {
			response.status = 404
			response.header.Set("Content-Type", "text/plain; charset=UTF-8")
			return []byte("404 Not Found"), nil
		}
		query, err := usageview.ParseQuery(honoQueryValues(r.URL.RawQuery), s.usage.Dates)
		if err != nil {
			response.status = 400
			return []byte(`{"error":"Invalid request"}`), nil
		}
		page, err := s.usage.QueryOn(r.Context(), conn, query)
		if err != nil {
			return nil, err
		}
		response.header.Set("X-Usage-Incomplete", strconv.FormatBool(page.Incomplete))
		response.header.Set("X-Usage-Planner-Incomplete", strconv.FormatBool(page.PlannerIncomplete))
		return jsjson.Marshal(page.Rows)
	}, func(body []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return response.enqueue(r.Context(), body)
	})
	if response.accepted {
		err = errors.Join(err, response.drain(r.Context(), w))
	}
	if err != nil {
		fail(err)
	}
}
