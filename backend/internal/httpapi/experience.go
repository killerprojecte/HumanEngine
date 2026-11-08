package httpapi

import (
	"database/sql"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/experienceservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

var experienceDecimal = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

const experiencePrefix = "/api/memory-experience"

func experiencePath(path string) bool {
	return path == experiencePrefix || strings.HasPrefix(path, experiencePrefix+"/")
}
func experienceRoute(r *http.Request) (operation, id string, ok bool) {
	path := habitRoutingPath(r)
	if path == experiencePrefix {
		return "list", "", r.Method == "GET" || r.Method == "HEAD"
	}
	tail := strings.TrimPrefix(path, experiencePrefix+"/")
	parts := strings.Split(tail, "/")
	if len(parts) == 1 && parts[0] != "" && r.Method == "DELETE" {
		return "delete", habitTryDecode(parts[0], false), true
	}
	if len(parts) == 2 && parts[0] != "" && r.Method == "POST" && (parts[1] == "validate" || parts[1] == "review") {
		return parts[1], habitTryDecode(parts[0], false), true
	}
	return "", "", false
}
func experienceID(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	n, e := jsjson.CodePointCount(s)
	return s, e == nil && n >= 1 && n <= 2048
}
func experienceMember(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func experienceQuery(raw string) (experienceservice.HTTPRequest, bool) {
	request := experienceservice.HTTPRequest{Operation: "list"}
	values := map[string][]string{}
	// Hono query decoding preserves malformed percent groups, ignores empty keys,
	// and presents repeated values as arrays to the strict Zod schema.
	for _, pair := range strings.Split(raw, "&") {
		k, v, _ := strings.Cut(pair, "=")
		k = habitTryDecode(strings.ReplaceAll(k, "+", " "), false)
		v = habitTryDecode(strings.ReplaceAll(v, "+", " "), false)
		if k != "" {
			values[k] = append(values[k], v)
		}
	}
	for k, v := range values {
		if !experienceMember(k, "sceneId", "status", "limit") || len(v) != 1 {
			return request, false
		}
	}
	scenes := values["sceneId"]
	if len(scenes) != 1 {
		return request, false
	}
	var ok bool
	request.SceneID, ok = experienceID(scenes[0])
	if !ok {
		return request, false
	}
	if s, has := values["status"]; has {
		if !experienceMember(s[0], "candidate", "validated", "active", "suspended", "retired") {
			return request, false
		}
		request.Options.Status = s[0]
	}
	if limits, has := values["limit"]; has {
		text := strings.TrimFunc(limits[0], func(r rune) bool {
			return r == '\uFEFF' || r == '\u00A0' || r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == '\u1680' || r >= 0x2000 && r <= 0x200a || r == '\u2028' || r == '\u2029' || r == '\u202f' || r == '\u205f' || r == '\u3000'
		})
		n := float64(0)
		var e error
		if len(text) > 2 && text[0] == '0' && strings.ContainsAny(text[1:2], "xXbBoO") {
			var u uint64
			u, e = strconv.ParseUint(text, 0, 64)
			n = float64(u)
		} else if text != "" {
			if !experienceDecimal.MatchString(text) {
				return request, false
			}
			n, e = strconv.ParseFloat(text, 64)
		}
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > 40 || math.Trunc(n) != n {
			return request, false
		}
		limit := int(n)
		request.Options.Limit = &limit
	}
	return request, true
}
func experienceBody(operation, id string, value any) (experienceservice.HTTPRequest, bool) {
	req := experienceservice.HTTPRequest{Operation: operation, ID: id}
	if _, ok := experienceID(id); !ok {
		return req, false
	}
	o, ok := value.(*jsjson.Object)
	if !ok {
		return req, false
	}
	size := 2
	if operation == "review" {
		size = 3
	}
	if o.Len() != size {
		return req, false
	}
	scene, _ := o.Get("sceneId")
	req.SceneID, ok = experienceID(scene)
	if !ok {
		return req, false
	}
	version, _ := o.Get("expectedVersion")
	n, ok := version.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 9007199254740991 || math.Trunc(n) != n {
		return req, false
	}
	req.ExpectedVersion = int64(n)
	if operation == "review" {
		v, _ := o.Get("status")
		req.Status, ok = v.(string)
		if !ok || !experienceMember(req.Status, "active", "suspended", "retired") {
			return req, false
		}
	}
	return req, true
}
func (s *Server) serveExperience(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	report := func(e error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "experience.failed", Message: e.Error(), RequestID: requestID})
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
		failure(errors.New("experience_request_unavailable"))
		return
	}
	publish := func(status int, body []byte) error {
		response.status = status
		return s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return body, nil }, func(b []byte) error { return response.enqueue(r.Context(), b) })
	}
	operation, id, route := experienceRoute(r)
	var err error
	if !route {
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
		err = publish(404, []byte("404 Not Found"))
	} else {
		// Match authenticated readiness before any potentially slow body read.
		err = s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil })
		if err != nil {
			failure(err)
			return
		}
		var request experienceservice.HTTPRequest
		valid := false
		if operation == "list" {
			request, valid = experienceQuery(r.URL.RawQuery)
		} else {
			if _, ok := experienceID(id); !ok {
				err = publish(400, []byte(`{"error":"Invalid request"}`))
				goto drain
			}
			var value any
			if !habitJSONContentType.MatchString(r.Header.Get("Content-Type")) {
				failure(errors.New("experience_json_content_type_required"))
				return
			}
			{
				raw, e := io.ReadAll(r.Body)
				if e != nil {
					failure(e)
					return
				}
				value, e = jsjson.Parse(string(raw))
				if e != nil {
					failure(e)
					return
				}
			}
			request, valid = experienceBody(operation, id, value)
		}
		if !valid {
			err = publish(400, []byte(`{"error":"Invalid request"}`))
		} else {
			err = s.experience.PublishHTTP(r.Context(), request, func(status int, body []byte) error {
				response.status = status
				return response.enqueue(r.Context(), body)
			})
		}
	}
drain:
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
