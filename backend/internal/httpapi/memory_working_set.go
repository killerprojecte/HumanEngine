package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const memoryWorkingSetPath = "/api/memory-working-set"

func (s *Server) WithMemoryWorkingSetRead(publisher *recovery.ReadPublisher) error {
	if s.memoryDelete == nil {
		return errors.New("http_memory_working_set_owner_invalid")
	}
	read, err := s.memoryDelete.BindWorkingSetAdminRead(publisher, s.schemas)
	if err == nil {
		s.workingSetRead = read
	}
	return err
}

func workingSetQuery(query url.Values) *memoryservice.WorkingSetQuery {
	includeCold := false
	if values, present := query["includeCold"]; present {
		if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
			return nil
		}
		includeCold = values[0] == "true"
		delete(query, "includeCold")
	}
	q := episodesQuery(query)
	if q == nil {
		return nil
	}
	return &memoryservice.WorkingSetQuery{EpisodesQuery: *q, IncludeCold: includeCold}
}

func (s *Server) serveMemoryWorkingSet(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_working_set.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.workingSetRead(r.Context(), workingSetQuery(honoQueryValues(r.URL.RawQuery)), emit)
	})
}
