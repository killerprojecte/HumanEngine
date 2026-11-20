package httpapi

import (
	"net/http"
	"net/url"

	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

func memoryRelationsReadPath(method, path string) bool {
	return path == "/api/memory-relations" && (method == http.MethodGet || method == http.MethodHead)
}

func memoryRelationsQuery(values url.Values) *memoryservice.RelationsQuery {
	out := &memoryservice.RelationsQuery{}
	for key, items := range values {
		if len(items) != 1 {
			return nil
		}
		v := items[0]
		switch key {
		case "sceneId":
			out.SceneID = memoryReadID(v)
		case "personId":
			out.List.PersonID = memoryReadID(v)
			if out.List.PersonID == "" {
				return nil
			}
		case "status":
			if v != "candidate" && v != "accepted" && v != "rejected" {
				return nil
			}
			out.List.Status = v
		case "limit":
			n, ok := coerceQueryInteger(v, 40)
			if !ok {
				return nil
			}
			limit := float64(n)
			out.List.Limit = &limit
		default:
			return nil
		}
	}
	if out.SceneID == "" {
		return nil
	}
	return out
}

func (s *Server) serveMemoryRelations(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_relations.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.memoryAdminRead.PublishRelations(r.Context(), memoryRelationsQuery(honoQueryValues(r.URL.RawQuery)), s.schemas, emit)
	})
}
