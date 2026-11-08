package httpapi

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var habitJSONContentType = regexp.MustCompile(`(?i)^application/([a-z-.]+\+)?json(;\s*[a-zA-Z0-9-]+\=([^;]+))*$`)

func habitPath(path string) bool {
	return path == "/api/habits" || strings.HasPrefix(path, "/api/habits/")
}

// Hono utils/url.js getPath protects percent, decodes URI (not URI-component),
// and recovers invalid UTF-8 by independently decoding contiguous %xx groups.
var habitPercentGroups = regexp.MustCompile(`(?:%[0-9A-Fa-f]{2})+`)
var habitPercentByte = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

func habitDecode(s string, uri bool) (string, bool) {
	if uri {
		s = habitPercentByte.ReplaceAllStringFunc(s, func(m string) string {
			b, e := url.PathUnescape(m)
			if e == nil && len(b) == 1 && strings.ContainsAny(b, ";/?:@&=+$,#") {
				return "%25" + m[1:]
			}
			return m
		})
	}
	v, e := url.PathUnescape(s)
	return v, e == nil && utf8.ValidString(v)
}
func habitTryDecode(s string, uri bool) string {
	if v, ok := habitDecode(s, uri); ok {
		return v
	}
	return habitPercentGroups.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := habitDecode(m, uri); ok {
			return v
		}
		return m
	})
}
func habitRoutingPath(r *http.Request) string {
	return habitTryDecode(strings.ReplaceAll(r.URL.EscapedPath(), "%25", "%2525"), true)
}

func habitRoute(r *http.Request) (string, bool) {
	// Hono matches the escaped route before decoding each parameter. An escaped
	// slash is part of one id, not an additional route segment.
	path := habitRoutingPath(r)
	if path == "/api/habits" {
		return "", r.Method == "GET" || r.Method == "HEAD"
	}
	raw := strings.TrimPrefix(path, "/api/habits/")
	if raw == "" || strings.Contains(raw, "/") || (r.Method != "PATCH" && r.Method != "DELETE") {
		return "", false
	}
	return habitTryDecode(raw, false), true
}

func habitError(w http.ResponseWriter, status int, message, id string) {
	o := new(jsjson.Object)
	_ = o.Set("error", message)
	if id != "" {
		_ = o.Set("requestId", id)
	}
	body, _ := jsjson.Stringify(o)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
func (s *Server) serveHabits(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	failure := errors.New("habit_request_unavailable")
	report := func(e error) {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "habits.failed", Message: e.Error(), RequestID: requestID, Fields: map[string]any{"path": r.URL.Path, "method": r.Method}})
		}
	}
	fail := func() { report(failure); habitError(w, 503, "Request failed; inspect server diagnostics", requestID) }
	if !available {
		fail()
		return
	}
	id, route := habitRoute(r)
	response := newPublicationResponse(w.Header(), r.Method == "HEAD")
	emit := func(body []byte) error { return response.enqueue(r.Context(), body) }
	if !route {
		// A known, body-free 404 needs only one reservation: readiness and
		// acceptance are atomic, with all transport work after its release.
		response.status = http.StatusNotFound
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
		err := s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) {
			return []byte("404 Not Found"), nil
		}, emit)
		if response.accepted {
			if drainErr := response.drain(r.Context(), w); err == nil {
				err = drainErr
			}
		}
		if err != nil {
			if !response.accepted && r.Context().Err() == nil {
				failure = err
				fail()
			} else {
				report(err)
			}
		}
		return
	}
	// Source authenticated middleware checks readiness before body/schema parsing.
	if e := s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil }); e != nil {
		failure = e
		fail()
		return
	}
	var err error
	switch r.Method {
	case "GET", "HEAD":
		err = s.habits.PublishAdminList(r.Context(), emit)
	case "DELETE":
		err = s.habits.DeleteAndPublish(r.Context(), id, emit)
	case "PATCH":
		if !habitJSONContentType.MatchString(r.Header.Get("Content-Type")) {
			fail()
			return
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			fail()
			return
		}
		v, e := jsjson.Parse(string(raw))
		if e != nil {
			fail()
			return
		}
		o, ok := v.(*jsjson.Object)
		valid := ok && o.Len() == 1
		if valid {
			locked, present := o.Get("locked")
			_, isBool := locked.(bool)
			valid = present && isBool
		}
		if !valid {
			response.status = http.StatusBadRequest
			err = s.authority.Publish(r.Context(), s.main, func(*sql.Conn) ([]byte, error) { return []byte(`{"error":"Invalid request"}`), nil }, emit)
			break
		}
		err = s.habits.UpdateAndPublish(r.Context(), id, o, emit)
	}
	if response.accepted {
		drainErr := response.drain(r.Context(), w)
		if err == nil {
			err = drainErr
		}
	}
	if err != nil {
		if !response.accepted && r.Context().Err() == nil {
			failure = err
			fail()
		} else {
			report(err)
		}
	}
}
