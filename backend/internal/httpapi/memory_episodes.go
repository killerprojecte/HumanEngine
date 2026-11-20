package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const memoryEpisodesPath = "/api/memory-episodes"

func (s *Server) WithMemoryEpisodesRead(publisher *recovery.ReadPublisher) error {
	if s.memoryDelete == nil {
		return errors.New("http_memory_episodes_owner_invalid")
	}
	read, err := s.memoryDelete.BindEpisodesAdminRead(publisher, s.schemas)
	if err == nil {
		s.episodesRead = read
	}
	return err
}

func episodesQuery(query url.Values) *memoryservice.EpisodesQuery {
	out := new(memoryservice.EpisodesQuery)
	for key, values := range query {
		if len(values) != 1 {
			return nil
		}
		id := values[0]
		n, err := jsjson.CodePointCount(id)
		if err != nil || n < 1 || n > 512 {
			return nil
		}
		switch key {
		case "sceneId":
			out.SceneID = id
		case "personId":
			out.PersonID = id
		default:
			return nil
		}
	}
	if out.SceneID == "" {
		return nil
	}
	return out
}

func (s *Server) serveMemoryEpisodes(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_episodes.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.episodesRead(r.Context(), episodesQuery(honoQueryValues(r.URL.RawQuery)), emit)
	})
}
