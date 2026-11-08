package httpapi

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modeldiagnostics"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"net/http"
	"strings"
)

func diagnosticsPath(path string) bool {
	return path == "/api/diagnostics" || strings.HasPrefix(path, "/api/diagnostics/")
}

type DiagnosticReader interface {
	OwnedBy(*sql.DB, *recovery.Authority) bool
	ReadRows() ([]modeldiagnostics.Row, error)
}

func (s *Server) WithDiagnostics(owner DiagnosticReader, publisher *recovery.ReadPublisher) error {
	if owner == nil || !owner.OwnedBy(s.main, s.authority) || !publisher.OwnedBy(s.main, s.authority) {
		return errors.New("http_diagnostic_owner_invalid")
	}
	s.diagnostics = owner
	s.diagnosticPublisher = publisher
	return nil
}
func (s *Server) serveDiagnostics(w http.ResponseWriter, r *http.Request, available bool, id string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	fail := func(err error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "diagnostics.failed", Message: "Diagnostic read failed", RequestID: id})
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
	err := s.diagnosticPublisher.Publish(r.Context(), func(*sql.Conn) ([]byte, error) {
		if habitRoutingPath(r) != "/api/diagnostics" || (r.Method != "GET" && r.Method != "HEAD") {
			response.status = 404
			response.header.Set("Content-Type", "text/plain; charset=UTF-8")
			return []byte("404 Not Found"), nil
		}
		rows, err := s.diagnostics.ReadRows()
		if err != nil {
			return nil, err
		}
		items := make([]any, 0, len(rows))
		for _, row := range rows {
			item, err := jsjson.NewObject(jsjson.Member{Key: "id", Value: row.ID}, jsjson.Member{Key: "at", Value: row.At}, jsjson.Member{Key: "modelId", Value: row.ModelID}, jsjson.Member{Key: "role", Value: row.Role}, jsjson.Member{Key: "input", Value: row.Input}, jsjson.Member{Key: "output", Value: row.Output})
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return jsjson.Marshal(items)
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
