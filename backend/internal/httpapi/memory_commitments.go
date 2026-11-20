package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const memoryCommitmentsPath = "/api/memory-commitments"

func (s *Server) WithMemoryCommitmentRead(publisher *recovery.ReadPublisher) error {
	if s.memoryDelete == nil {
		return errors.New("http_memory_commitment_owner_invalid")
	}
	read, err := s.memoryDelete.BindCommitmentAdminRead(publisher, s.schemas)
	if err == nil {
		s.commitmentRead = read
	}
	return err
}

func memoryCommitmentRoute(method, path string) (string, bool) {
	if method != http.MethodGet && method != http.MethodHead {
		return "", false
	}
	if path == memoryCommitmentsPath {
		return "", true
	}
	id, found := strings.CutPrefix(path, memoryCommitmentsPath+"/")
	return id, found && id != "" && !strings.Contains(id, "/")
}

func memoryCommitmentQuery(query url.Values, rawID string) *memoryservice.CommitmentAdminQuery {
	out := &memoryservice.CommitmentAdminQuery{}
	const maxSafe = 9007199254740991
	if rawID != "" {
		out.Ref.ID = memoryReadID(habitTryDecode(rawID, false))
		if out.Ref.ID == "" || len(query) != 3 {
			return nil
		}
	} else {
		life := "open"
		out.List.Lifecycle = &life
	}
	for key, values := range query {
		if len(values) != 1 {
			return nil
		}
		v := values[0]
		if key == "sceneId" {
			out.SceneID = memoryReadID(v)
			continue
		}
		if rawID != "" {
			switch key {
			case "version":
				n, ok := coerceQueryInteger(v, maxSafe)
				if !ok {
					return nil
				}
				out.Ref.Version = float64(n)
			case "deletionEpoch":
				n, ok := coerceQueryIntegerRange(v, 0, maxSafe)
				if !ok {
					return nil
				}
				out.Ref.DeletionEpoch = float64(n)
			default:
				return nil
			}
			continue
		}
		switch key {
		case "personId":
			out.List.PersonID = memoryReadID(v)
			if out.List.PersonID == "" {
				return nil
			}
		case "limit":
			n, ok := coerceQueryInteger(v, 12)
			if !ok {
				return nil
			}
			limit := float64(n)
			out.List.Limit = &limit
		case "lifecycle":
			if v != "open" && v != "terminal" && v != "all" {
				return nil
			}
			*out.List.Lifecycle = v
		default:
			return nil
		}
	}
	if out.SceneID == "" {
		return nil
	}
	return out
}

func (s *Server) serveMemoryCommitmentRead(w http.ResponseWriter, r *http.Request, available bool, requestID, rawID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_commitment_read.failed", func(emit func(int, []byte) error) (bool, error) {
		return s.commitmentRead(r.Context(), memoryCommitmentQuery(honoQueryValues(r.URL.RawQuery), rawID), emit)
	})
}
