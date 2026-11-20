package httpapi

import "net/http"

func (s *Server) serveMemoryIdentities(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_identities.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.memoryAdminRead.PublishIdentities(r.Context(), func(body []byte) error {
			return emit(http.StatusOK, body)
		})
	})
}
