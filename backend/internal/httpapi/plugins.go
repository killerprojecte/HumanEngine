package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"io"
	"net/http"
	"strings"
)

// WithPlugins opts into the real manager using the same durable owners. Preview
// constructors remain read-only and must not install a fake success manager.
func (s *Server) WithPlugins(manager *pluginmanager.Manager) error {
	if s == nil || !manager.OwnedBy(s.main, s.authority) {
		return errors.New("http_plugin_dependencies_invalid")
	}
	s.plugins = manager
	return nil
}
func pluginPath(path string) bool {
	return path == "/api/plugins" || strings.HasPrefix(path, "/api/plugins/")
}
func (s *Server) servePlugins(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	publish := func(status int, body []byte) error {
		response.status = status
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		return s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return body, nil }, func(raw []byte) error { return response.enqueue(r.Context(), raw) })
	}
	failure := func(err error) {
		if !response.accepted && r.Context().Err() == nil {
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
		failure(errors.New("plugin_request_unavailable"))
		return
	}
	path := habitRoutingPath(r)
	segments := strings.Split(strings.TrimPrefix(path, "/api/plugins/"), "/")
	list := path == "/api/plugins"
	var id, operation, action string
	if !list && len(segments) >= 2 {
		id = habitTryDecode(segments[0], false)
		operation = segments[1]
		if len(segments) == 3 && operation == "actions" {
			action = habitTryDecode(segments[2], false)
		}
	}
	read := r.Method == "GET" || r.Method == "HEAD"
	match := list && read || !list && id != "" && len(segments) == 2 && (operation == "config" && (read || r.Method == "PUT") || operation == "enabled" && r.Method == "PUT") || !list && id != "" && action != "" && len(segments) == 3 && operation == "actions" && r.Method == "POST"
	var result any
	var err error
	status := 200
	if !match {
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
		err = publish(404, []byte("404 Not Found"))
	} else {
		err = s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
		var input *jsjson.Object
		if err == nil && !read {
			if !habitJSONContentType.MatchString(r.Header.Get("Content-Type")) {
				failure(errors.New("plugin_json_content_type_required"))
				return
			}
			raw, e := io.ReadAll(r.Body)
			if e != nil {
				failure(e)
				return
			}
			value, e := jsjson.ParseBytes(raw)
			if e != nil {
				failure(e)
				return
			}
			input, _ = value.(*jsjson.Object)
			if input == nil {
				status = 400
			} else if operation == "enabled" {
				enabled, exists := input.Get("enabled")
				_, boolean := enabled.(bool)
				if !exists || !boolean || input.Len() != 1 {
					status = 400
				}
			}
		}
		if err == nil && status == 400 {
			err = publish(400, []byte(`{"error":"Invalid request"}`))
		} else if err == nil {
			switch {
			case list:
				result, err = s.plugins.List(r.Context())
			case operation == "config" && read:
				result, err = s.plugins.Config(r.Context(), id)
			case operation == "config":
				result, err = s.plugins.Configure(r.Context(), id, input, requestID)
			case operation == "enabled":
				enabled, _ := input.Get("enabled")
				if enabled == true {
					err = s.plugins.Enable(r.Context(), id, requestID)
				} else {
					err = s.plugins.Disable(r.Context(), id, requestID)
				}
				result, _ = jsjson.NewObject(jsjson.Member{Key: "ok", Value: true})
			case operation == "actions":
				result, err = s.plugins.Action(r.Context(), id, action, input)
			}
			if err != nil && (operation == "config" && !read || operation == "enabled") {
				var configuration *pluginmanager.ConfigurationError
				var startup *pluginmanager.StartupError
				if errors.As(err, &configuration) {
					status = 400
					result, _ = jsjson.NewObject(jsjson.Member{Key: "error", Value: "invalid_plugin_configuration"}, jsjson.Member{Key: "requestId", Value: requestID}, jsjson.Member{Key: "issues", Value: pluginmanager.PublicIssues(configuration.Issues)})
					err = nil
				} else if errors.As(err, &startup) {
					status = 503
					encoded, _ := json.Marshal(startup)
					result, err = jsjson.Parse(string(encoded))
				}
			}
			if err == nil {
				var raw []byte
				raw, err = jsjson.Marshal(result)
				if err == nil {
					err = publish(status, raw)
				}
			}
		}
	}
	if response.accepted {
		err = errors.Join(err, response.drain(r.Context(), w))
	}
	if err != nil {
		failure(err)
	}
}
