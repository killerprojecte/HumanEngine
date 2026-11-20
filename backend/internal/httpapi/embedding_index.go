package httpapi

import (
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/learningowner"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const embeddingIndexPath = "/api/embedding-index"

func (s *Server) WithEmbeddingIndexRead(owner *learningowner.Owner, publisher *recovery.ReadPublisher) error {
	read, err := owner.BindIndexAdminRead(publisher, s.schemas)
	if err == nil {
		s.embeddingIndexRead = read
	}
	return err
}

func (s *Server) serveEmbeddingIndex(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "embedding_index.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.embeddingIndexRead(r.Context(), func(body []byte) error { return emit(http.StatusOK, body) })
	})
}
