package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// WithProviderReads binds the same live native model owner used by runtime calls.
// Configure before serving. New/Open remain uncomposed read-only previews.
func (s *Server) WithProviderReads(models *providertransport.NativeModels, publisher *recovery.ReadPublisher) error {
	if s == nil || models == nil || models.Policy == nil || models.Client == nil || models.Host == nil || !publisher.OwnedBy(s.main, s.authority) {
		return errors.New("http_provider_reads_dependencies_invalid")
	}
	s.providerReads = models
	s.providerPublisher = publisher
	return nil
}
func providerReadPath(path string) bool {
	return path == "/api/model-protocols" || strings.HasPrefix(path, "/api/model-protocols/") || strings.HasPrefix(path, "/api/providers/")
}
func (s *Server) serveProviderReads(w http.ResponseWriter, r *http.Request, available bool, id string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	fail := func(err error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "provider_read.failed", Message: "Provider read failed", RequestID: id})
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
	publish := func(read func(*sql.Conn) ([]byte, error)) error {
		return s.providerPublisher.Publish(r.Context(), read, func(body []byte) error {
			response.header.Set("Cache-Control", "no-store")
			response.header.Set("X-Content-Type-Options", "nosniff")
			return response.enqueue(r.Context(), body)
		})
	}
	path := habitRoutingPath(r)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	var err error
	if r.Method != "GET" && r.Method != "HEAD" {
		response.status = 404
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
		err = publish(func(*sql.Conn) ([]byte, error) { return []byte("404 Not Found"), nil })
	} else if path == "/api/model-protocols" {
		err = publish(func(*sql.Conn) ([]byte, error) {
			rows := []any{}
			for _, protocol := range s.providerReads.Protocols() {
				operations := []any{}
				for _, op := range protocol.Operations {
					operations = append(operations, op)
				}
				row, e := jsjson.NewObject(jsjson.Member{Key: "id", Value: protocol.ID}, jsjson.Member{Key: "label", Value: protocol.Label}, jsjson.Member{Key: "operations", Value: operations})
				if e != nil {
					return nil, e
				}
				rows = append(rows, row)
			}
			return jsjson.Marshal(rows)
		})
	} else {
		raw := strings.TrimPrefix(path, "/api/providers/")
		valid := strings.HasPrefix(path, "/api/providers/") && strings.HasSuffix(raw, "/models")
		raw = strings.TrimSuffix(raw, "/models")
		if !valid || raw == "" || strings.Contains(raw, "/") {
			response.status = 404
			response.header.Set("Content-Type", "text/plain; charset=UTF-8")
			err = publish(func(*sql.Conn) ([]byte, error) { return []byte("404 Not Found"), nil })
		} else {
			var providerID string
			providerID, err = url.PathUnescape(raw)
			if err == nil {
				// Never hold a SQLite snapshot or the authority mutex across provider IO.
				err = s.providerPublisher.Publish(r.Context(), func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
			}
			var models []string
			if err == nil {
				models, err = s.providerReads.ListModels(r.Context(), providerID)
			}
			if err == nil {
				err = publish(func(*sql.Conn) ([]byte, error) {
					items := []any{}
					for _, model := range models {
						items = append(items, model)
					}
					return marshalProjection(map[string]any{"models": items})
				})
			}
		}
	}
	if response.accepted {
		err = errors.Join(err, response.drain(r.Context(), w))
	}
	if err != nil {
		fail(err)
	}
}
