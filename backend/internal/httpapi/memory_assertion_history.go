package httpapi

import (
	"net/http"
	"net/url"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

const assertionHistoryPath = "/api/memory-assertion-history"

func assertionHistoryReadPath(method, path string) bool {
	return path == assertionHistoryPath && (method == http.MethodGet || method == http.MethodHead)
}

func assertionHistoryQuery(values url.Values) *memoryservice.AssertionHistoryQuery {
	out := &memoryservice.AssertionHistoryQuery{}
	for key, items := range values {
		if len(items) != 1 {
			return nil
		}
		v := items[0]
		switch key {
		case "sceneId":
			out.SceneID = memoryReadID(v)
		case "memoryId":
			out.ID = memoryReadID(v)
		case "asOfRecorded", "validAt":
			if !assertions.ValidHistoryInstant(v) {
				return nil
			}
			if key == "asOfRecorded" {
				out.AsOfRecorded = v
			} else {
				out.ValidAt = v
			}
		case "limit":
			n, ok := coerceQueryInteger(v, 20)
			if !ok {
				return nil
			}
			out.Limit = int(n)
		default:
			return nil
		}
	}
	if out.SceneID == "" || out.ID == "" {
		return nil
	}
	return out
}

func (s *Server) serveMemoryAssertionHistory(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_assertion_history.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.memoryAdminRead.PublishAssertionHistory(r.Context(), assertionHistoryQuery(honoQueryValues(r.URL.RawQuery)), s.schemas, emit)
	})
}
