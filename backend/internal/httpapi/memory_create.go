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

func (s *Server) serveMemoryCreate(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	ctx := r.Context()
	fail := func() {
		if s.logs != nil {
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "memory.create.failed", Message: "Memory creation failed", RequestID: requestID})
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
	input, err := s.schemas.ParseMemoryCreate(value)
	if err != nil {
		habitError(w, 400, "Invalid request", "")
		return
	}
	record, err := s.memoryDelete.Create(ctx, input)
	if err != nil {
		fail()
		return
	}
	id, _ := record.Get("id")
	content, _ := record.Get("content")
	s.manualMemoryIndex(ctx, id.(string), content.(string))
	response := newPublicationResponse(w.Header(), false)
	err = s.memoryAdminRead.PublishCreated(ctx, id.(string), func(body []byte) error {
		return response.enqueue(ctx, body)
	})
	if response.accepted {
		_ = response.drain(ctx, w)
	} else if err != nil {
		fail()
	}
}
