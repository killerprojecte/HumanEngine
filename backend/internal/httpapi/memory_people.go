package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

const memoryPeoplePath = "/api/memory-people"

func peopleReadPath(method, path string) bool {
	return (method == http.MethodGet || method == http.MethodHead) &&
		(path == memoryPeoplePath || path == memoryPeoplePath+"/profile")
}

func peopleQuery(query url.Values, profile bool) *memoryservice.PeopleQuery {
	want := 1
	if profile {
		want++
	}
	if len(query) != want {
		return nil
	}
	identifier := func(key string) string {
		values := query[key]
		if len(values) == 1 {
			if n, err := jsjson.CodePointCount(values[0]); err == nil && n >= 1 && n <= 2048 {
				return values[0]
			}
		}
		return ""
	}
	scene := identifier("sceneId")
	if scene == "" {
		return nil
	}
	person := ""
	if profile {
		person = identifier("personId")
		if person == "" {
			return nil
		}
	}
	return &memoryservice.PeopleQuery{SceneID: scene, PersonID: person}
}

func (s *Server) serveMemoryPeople(w http.ResponseWriter, r *http.Request, available bool, requestID string, profile bool) {
	ctx := r.Context()
	response := newPublicationResponse(w.Header(), r.Method == http.MethodHead)
	applyReadHeaders := func(h http.Header) {
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
	}
	var started bool
	var err error
	if available {
		started, err = s.memoryAdminRead.PublishPeople(ctx, peopleQuery(honoQueryValues(r.URL.RawQuery), profile), func(status int, body []byte) error {
			response.status = status
			applyReadHeaders(response.header)
			return response.enqueue(ctx, body)
		})
		if response.accepted {
			err = errors.Join(err, response.drain(ctx, w))
		}
		if err == nil {
			return
		}
	}
	if s.logs != nil {
		_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "memory_people.failed", Message: "Memory people read failed", RequestID: requestID})
	}
	if !response.accepted && ctx.Err() == nil {
		if started {
			applyReadHeaders(w.Header())
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			habitError(w, http.StatusServiceUnavailable, "Request failed; inspect server diagnostics", requestID)
		}
	}
}
