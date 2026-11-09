package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
)

type SceneConfigurationRuntime interface {
	SceneChanged(context.Context, *jsjson.Object) error
}
type sceneConfigurationOwner struct {
	store     *scenestore.Store
	runtime   SceneConfigurationRuntime
	admission chan struct{}
}

func (s *Server) WithSceneConfiguration(store *scenestore.Store, runtime SceneConfigurationRuntime) error {
	if s == nil || store == nil || store.Main != s.main || store.Authority != s.authority || runtime == nil {
		return errors.New("http_scene_configuration_dependencies_invalid")
	}
	s.sceneConfiguration = &sceneConfigurationOwner{store: store, runtime: runtime, admission: make(chan struct{}, 1)}
	return nil
}
func sceneConfigurationPath(path string) bool { return strings.HasPrefix(path, "/api/scenes/") }
func (s *Server) serveSceneConfiguration(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	failure := func(err error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "scene_configuration.failed", Message: err.Error(), RequestID: requestID})
		}
		if !response.accepted && r.Context().Err() == nil {
			if available {
				if e := s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil }); e == nil {
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
		failure(errors.New("scene_configuration_request_unavailable"))
		return
	}
	publish := func(status int, body []byte) error {
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		response.status = status
		return s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return body, nil }, func(b []byte) error { return response.enqueue(r.Context(), b) })
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/scenes/")
	var err error
	if r.Method != "PUT" || path == "" || strings.Contains(path, "/") {
		err = publish(404, []byte("404 Not Found"))
	} else {
		id, e := url.PathUnescape(path)
		if e != nil {
			failure(e)
			return
		}
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
		scene, e := s.schemas.ParseOrdered("Scene", raw)
		if e != nil {
			err = publish(400, []byte(`{"error":"Invalid request"}`))
		} else if actual, _ := scene.Get("id"); actual != id {
			err = publish(400, []byte(`{"error":"Scene ID mismatch"}`))
		} else {
			owner := s.sceneConfiguration
			select {
			case owner.admission <- struct{}{}:
			case <-r.Context().Done():
				return
			}
			func() {
				defer func() { <-owner.admission }()
				if _, err = owner.store.Save(r.Context(), scene); err != nil {
					return
				}
				// A committed update cannot skip its source effects on client cancellation.
				effectContext := context.WithoutCancel(r.Context())
				var saved *jsjson.Object
				sqlID, e := jsjson.UTF8Text(id)
				if e != nil {
					err = e
					return
				}
				err = s.authority.Publish(effectContext, s.main, func(c *sql.Conn) ([]byte, error) {
					var text string
					e := c.QueryRowContext(effectContext, "SELECT value FROM scenes WHERE id=?", sqlID).Scan(&text)
					return []byte(text), e
				}, func(raw []byte) error {
					v, e := jsjson.ParseBytes(raw)
					if e != nil {
						return e
					}
					var ok bool
					saved, ok = v.(*jsjson.Object)
					if !ok {
						return errors.New("scene_stored_value_invalid")
					}
					return nil
				})
				if err != nil {
					return
				}
				if err = owner.runtime.SceneChanged(effectContext, saved); err != nil {
					return
				}
				raw, e := jsjson.Marshal(saved)
				if e != nil {
					err = e
					return
				}
				err = publish(200, raw)
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
