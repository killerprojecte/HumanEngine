package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/settingsstore"
)

// SettingsChange is emitted only after the settings writer reports a definite
// commit. Runtime owners must apply all source settingsChanged effects before
// returning, and retain their task/lifetime admission through the callback.
type SettingsChange struct{ Previous, Current *jsjson.Object }
type ConfigurationRuntime interface {
	SettingsChanged(context.Context, SettingsChange) error
}
type configurationOwner struct {
	secrets   *credentials.Store
	settings  *settingsstore.Store
	runtime   ConfigurationRuntime
	admission chan struct{}
}

// WithConfiguration is an explicit writer opt-in. Read-only preview constructors
// remain read-only. Runtime callbacks may not be substituted with successful
// no-ops; they are the application's concrete settings-change boundary.
func (s *Server) WithConfiguration(secrets *credentials.Store, settings *settingsstore.Store, runtime ConfigurationRuntime) error {
	if s == nil || !secrets.OwnedBy(s.main, s.authority) || settings == nil || settings.Main != s.main || settings.Authority != s.authority || runtime == nil {
		return errors.New("http_configuration_dependencies_invalid")
	}
	s.configuration = &configurationOwner{secrets: secrets, settings: settings, runtime: runtime, admission: make(chan struct{}, 1)}
	return nil
}
func configurationPath(path string) bool {
	return path == "/api/settings" || path == "/api/models" || strings.HasPrefix(path, "/api/settings/") || strings.HasPrefix(path, "/api/models/")
}
func (s *Server) serveConfiguration(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	failure := func(err error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "configuration.failed", Message: err.Error(), RequestID: requestID})
		}
		if !response.accepted && r.Context().Err() == nil {
			// Hono's error handler rejoins authenticated middleware when recovery
			// remains ready. A durable intent gap aborts that middleware before these
			// success-path security headers are installed.
			if available {
				ready := s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
				if ready == nil {
					w.Header().Set("Cache-Control", "no-store")
					w.Header().Set("X-Content-Type-Options", "nosniff")
				}
			}
			if r.Method == "HEAD" {
				w.WriteHeader(503)
			} else {
				habitError(w, 503, "Request failed; inspect server diagnostics", requestID)
			}
		}
	}
	if !available {
		failure(errors.New("configuration_request_unavailable"))
		return
	}
	publish := func(status int, body []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		response.status = status
		return s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return body, nil }, func(b []byte) error { return response.enqueue(r.Context(), b) })
	}
	path := habitRoutingPath(r)
	var err error
	if (path != "/api/settings" && path != "/api/models") || (r.Method != "GET" && r.Method != "HEAD" && r.Method != "PUT") {
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
		err = publish(404, []byte("404 Not Found"))
	} else if r.Method != "PUT" {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		err = s.publishResponse(r.Context(), path, response)
	} else {
		err = s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
		if err != nil {
			failure(err)
			return
		}
		if !habitJSONContentType.MatchString(r.Header.Get("Content-Type")) {
			failure(errors.New("configuration_json_content_type_required"))
			return
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			failure(e)
			return
		}
		if _, e = jsjson.ParseBytes(raw); e != nil {
			failure(e)
			return
		}
		var parsed, mutations *jsjson.Object
		if path == "/api/models" {
			parsed, mutations, e = s.schemas.ParseModelWrite(raw)
		} else {
			parsed, e = s.schemas.ParseOrdered("Settings", raw)
		}
		if e != nil {
			err = publish(400, []byte(`{"error":"Invalid request"}`))
		} else {
			owner := s.configuration
			select {
			case owner.admission <- struct{}{}:
			case <-r.Context().Done():
				return
			}
			// Serialize the settings read, history, commit and runtime effects, not body
			// reads or socket writes. This is not a global bot work queue.
			func() {
				defer func() { <-owner.admission }()
				if path == "/api/models" {
					_, err = owner.secrets.SaveModels(r.Context(), parsed, mutations)
				} else {
					var previous *jsjson.Object
					err = s.authority.Publish(r.Context(), s.main, func(conn *sql.Conn) ([]byte, error) {
						b, e := s.projection(r.Context(), conn, path)
						if e == nil {
							previous, e = s.schemas.ParseOrdered("Settings", b)
						}
						return nil, e
					}, func([]byte) error { return nil })
					if err == nil {
						err = owner.settings.RecordPersona(r.Context(), parsed)
					}
					if err == nil {
						_, err = owner.settings.Set(r.Context(), parsed)
					}
					// Cancellation cannot undo a definite commit or skip its runtime effects.
					// The enclosing application's request admission keeps dependencies alive.
					if err == nil {
						err = owner.runtime.SettingsChanged(context.WithoutCancel(r.Context()), SettingsChange{Previous: previous, Current: parsed})
					}
				}
				if err != nil {
					return
				}
				if s.logs != nil {
					code := "models_updated"
					if path == "/api/settings" {
						code = "settings_updated"
					}
					_ = s.logs.Emit(observability.Event{Level: "info", Module: "config", Code: code, RequestID: requestID})
				}
				if path == "/api/settings" {
					var b []byte
					b, err = jsjson.Marshal(parsed)
					if err == nil {
						err = publish(200, b)
					}
				} else {
					response.header.Set("Cache-Control", "no-store")
					response.header.Set("X-Content-Type-Options", "nosniff")
					err = s.publishResponse(r.Context(), path, response)
				}
			}()
		}
	}
	if response.accepted {
		err = errors.Join(err, response.drain(r.Context(), w))
	}
	if err != nil {
		failure(err)
	}
}
