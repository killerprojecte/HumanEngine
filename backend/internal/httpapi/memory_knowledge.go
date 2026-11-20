package httpapi

import (
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"golang.org/x/text/encoding/unicode"
)

func (s *Server) WithMemoryKnowledgeRead(publisher *recovery.ReadPublisher) error {
	read, err := s.memoryDelete.BindKnowledgeAdminRead(publisher, s.schemas)
	if err == nil {
		s.knowledgeRead = read
	}
	return err
}

func memoryKnowledgeQuery(r *http.Request) *knowledge.AdminQuery {
	if !habitJSONContentType.MatchString(r.Header.Get("Content-Type")) {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	if !utf8.Valid(raw) {
		raw, err = unicode.UTF8.NewDecoder().Bytes(raw)
		if err != nil {
			return nil
		}
	}
	value, err := jsjson.ParseBytes(raw)
	if err != nil {
		return nil
	}
	q, _ := knowledge.ParseAdminQuery(value)
	return q
}

func (s *Server) serveMemoryKnowledge(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_knowledge.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.knowledgeRead(r.Context(), memoryKnowledgeQuery(r), emit)
	})
}
