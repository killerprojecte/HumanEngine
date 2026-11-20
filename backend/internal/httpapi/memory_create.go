package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"golang.org/x/text/encoding/unicode"
)

func (s *Server) WithMemoryCreate(index func(context.Context, string, string)) error {
	if s.memoryAdminRead == nil || index == nil {
		return errors.New("http_memory_create_owner_invalid")
	}
	s.manualMemoryIndex = index
	return nil
}

func (s *Server) serveMemoryWrite(w http.ResponseWriter, r *http.Request, available bool, requestID, memoryID string) {
	ctx := r.Context()
	operation := "create"
	if r.Method == http.MethodPatch {
		operation = "update"
	}
	fail := func() {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "memory." + operation + ".failed", Message: "Memory write failed", RequestID: requestID})
		}
		if ctx.Err() == nil {
			habitError(w, 503, "Request failed; inspect server diagnostics", requestID)
		}
	}
	if !available {
		fail()
		return
	}
	if err := s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil }); err != nil {
		fail()
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !habitJSONContentType.MatchString(r.Header.Get("Content-Type")) {
		fail()
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		fail()
		return
	}
	if !utf8.Valid(raw) {
		raw, err = unicode.UTF8.NewDecoder().Bytes(raw)
		if err != nil {
			fail()
			return
		}
	}
	value, err := jsjson.ParseBytes(raw)
	if err != nil {
		fail()
		return
	}
	response := newPublicationResponse(w.Header(), false)
	enqueue := func(body []byte) error { return response.enqueue(ctx, body) }
	if r.Method == http.MethodPatch {
		err = s.patchMemory(ctx, w, memoryID, value, enqueue)
	} else {
		err = s.createMemory(ctx, w, value, enqueue)
	}
	if response.accepted {
		_ = response.drain(ctx, w)
	} else if err != nil {
		fail()
	}
}

func (s *Server) createMemory(ctx context.Context, w http.ResponseWriter, value any, enqueue func([]byte) error) error {
	input, err := s.schemas.ParseMemoryCreate(value)
	if err != nil {
		habitError(w, 400, "Invalid request", "")
		return nil
	}
	record, err := s.memoryDelete.Create(ctx, input)
	if err != nil {
		return err
	}
	id, _ := record.Get("id")
	content, _ := record.Get("content")
	s.manualMemoryIndex(ctx, id.(string), content.(string))
	return s.memoryAdminRead.PublishCreated(ctx, id.(string), enqueue)
}
