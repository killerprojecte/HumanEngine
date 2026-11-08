package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelcalls"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"net/http"
	"strings"
)

func modelCallsPath(path string) bool {
	return path == "/api/model-calls" || strings.HasPrefix(path, "/api/model-calls/")
}

func (s *Server) WithModelCalls(store *modelcalls.Store, publisher *recovery.ReadPublisher) error {
	if !store.OwnedBy(s.main) || !publisher.OwnedBy(s.main, s.authority) {
		return errors.New("http_model_calls_owner_invalid")
	}
	s.modelCalls = store
	s.modelCallsPublisher = publisher
	return nil
}
func (s *Server) serveModelCalls(w http.ResponseWriter, r *http.Request, available bool, id string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	fail := func(err error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "model_calls.failed", Message: "Model-call history read failed", RequestID: id})
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
	err := s.modelCallsPublisher.Publish(r.Context(), func(conn *sql.Conn) ([]byte, error) {
		if habitRoutingPath(r) != "/api/model-calls" || (r.Method != "GET" && r.Method != "HEAD") {
			response.status = 404
			response.header.Set("Content-Type", "text/plain; charset=UTF-8")
			return []byte("404 Not Found"), nil
		}
		query, err := modelcalls.ParseQuery(r.URL.Query())
		if err != nil {
			response.status = 400
			return []byte(`{"error":"Invalid request"}`), nil
		}
		page, err := s.modelCalls.QueryOn(r.Context(), conn, query)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(page)
		if err != nil {
			return nil, err
		}
		ordered, err := jsjson.ParseBytes(raw)
		if err != nil {
			return nil, err
		}
		return jsjson.Marshal(ordered)
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
