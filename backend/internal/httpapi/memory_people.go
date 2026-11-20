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
		if values := query[key]; len(values) == 1 {
			return memoryReadID(values[0])
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

func memoryReadID(value string) string {
	if n, err := jsjson.CodePointCount(value); err == nil && n >= 1 && n <= 2048 {
		return value
	}
	return ""
}

func (s *Server) serveMemoryPeople(w http.ResponseWriter, r *http.Request, available bool, requestID string, profile bool) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_people.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.memoryAdminRead.PublishPeople(r.Context(), peopleQuery(honoQueryValues(r.URL.RawQuery), profile), emit)
	})
}

func (s *Server) serveMemoryScopedRead(w http.ResponseWriter, r *http.Request, available bool, requestID, code string, read func(func(int, []byte) error) (bool, error)) {
	ctx := r.Context()
	response := newPublicationResponse(w.Header(), r.Method == http.MethodHead)
	applyReadHeaders := func(h http.Header) {
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
	}
	var started bool
	var err error
	if available {
		started, err = read(func(status int, body []byte) error {
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
		_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: code, Message: "Scoped memory administration read failed", RequestID: requestID})
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
