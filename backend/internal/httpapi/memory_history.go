package httpapi

import (
	"net/http"
	"net/url"

	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

const memoryHistoryPath = "/api/memory-history"

func historyReadPath(method, path string) bool {
	return path == memoryHistoryPath && (method == http.MethodGet || method == http.MethodHead)
}

func basicHistoryQuery(query url.Values) *memoryservice.HistoryQuery {
	if len(query) != 2 || len(query["sceneId"]) != 1 || len(query["id"]) != 1 {
		return nil
	}
	scene, id := memoryReadID(query.Get("sceneId")), memoryReadID(query.Get("id"))
	if scene == "" || id == "" {
		return nil
	}
	return &memoryservice.HistoryQuery{SceneID: scene, ID: id}
}

func (s *Server) serveMemoryHistory(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_history.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.memoryAdminRead.PublishHistory(r.Context(), basicHistoryQuery(honoQueryValues(r.URL.RawQuery)), emit)
	})
}
