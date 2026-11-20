package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const memorySemanticsPath = "/api/memory-semantics"

func (s *Server) WithMemorySemanticsRead(publisher *recovery.ReadPublisher) error {
	if s.memoryDelete == nil {
		return errors.New("http_memory_semantics_owner_invalid")
	}
	read, err := s.memoryDelete.BindSemanticsAdminRead(publisher, s.schemas)
	if err == nil {
		s.semanticsRead = read
	}
	return err
}

func semanticsQuery(query url.Values) *memoryservice.SemanticsQuery {
	out := &memoryservice.SemanticsQuery{Limit: 12}
	for key, values := range query {
		if len(values) != 1 {
			return nil
		}
		switch key {
		case "sceneId":
			id := values[0]
			n, err := jsjson.CodePointCount(id)
			if err != nil || n < 1 || n > 512 {
				return nil
			}
			out.SceneID = id
		case "limit":
			n, ok := coerceQueryInteger(values[0], 40)
			if !ok {
				return nil
			}
			out.Limit = int(n)
		default:
			return nil
		}
	}
	if out.SceneID == "" {
		return nil
	}
	return out
}

func (s *Server) serveMemorySemantics(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_semantics.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.semanticsRead(r.Context(), semanticsQuery(honoQueryValues(r.URL.RawQuery)), emit)
	})
}
