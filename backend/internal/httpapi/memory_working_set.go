package httpapi

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"golang.org/x/text/encoding/unicode"
)

const (
	memoryWorkingSetPath        = "/api/memory-working-set"
	memoryWorkingSetRebuildPath = memoryWorkingSetPath + "/rebuild"
)

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

func workingSetRebuildQuery(r *http.Request) (*memoryservice.WorkingSetQuery, error) {
	media := r.Header.Get("Content-Type")
	if media != "" && !habitJSONContentType.MatchString(media) {
		return nil, errors.New("working_set_rebuild_unsupported_media")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if media == "" {
		if len(raw) == 0 {
			// Optional OpenAPI body: the source bypasses Zod here and
			// reaches the scene check with an undefined scene ID.
			return &memoryservice.WorkingSetQuery{IncludeCold: true}, nil
		}
		return nil, errors.New("working_set_rebuild_unsupported_media")
	}
	// Fetch decodes JSON bodies as UTF-8, ignoring an initial BOM.
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(raw) {
		raw, err = unicode.UTF8.NewDecoder().Bytes(raw)
		if err != nil {
			return nil, err
		}
	}
	value, err := jsjson.ParseBytes(raw)
	if err != nil {
		return nil, err
	}
	obj, ok := value.(*jsjson.Object)
	if !ok {
		return nil, nil
	}
	scene, _ := obj.Get("sceneId")
	person, hasPerson := obj.Get("personId")
	q := memoryservice.EpisodesQuery{SceneID: workspaceID(scene)}
	if q.SceneID == "" || (hasPerson && obj.Len() != 2) || (!hasPerson && obj.Len() != 1) {
		return nil, nil
	}
	if hasPerson {
		q.PersonID = workspaceID(person)
		if q.PersonID == "" {
			return nil, nil
		}
	}
	return &memoryservice.WorkingSetQuery{EpisodesQuery: q, IncludeCold: true}, nil
}

func (s *Server) serveMemoryWorkingSet(w http.ResponseWriter, r *http.Request, available bool, requestID string, rebuild bool) {
	ctx := r.Context()
	notFound := rebuild && r.Method != http.MethodPost
	if notFound && available {
		w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	}
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_working_set.failed", func(emit func(int, []byte) error) (bool, error) {
		if !rebuild {
			return s.workingSetRead(ctx, workingSetQuery(honoQueryValues(r.URL.RawQuery)), emit)
		}
		if notFound {
			err := s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) {
				return []byte("404 Not Found"), nil
			}, func(body []byte) error { return emit(http.StatusNotFound, body) })
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
			}
			return false, err
		}
		q, err := workingSetRebuildQuery(r)
		if err != nil {
			// The Hono media/JSON exceptions are rendered as diagnostic 503s
			// only after recovery readiness; Zod failures instead use owner 400.
			started := false
			err = s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) {
				started = true
				return nil, err
			}, func([]byte) error { return nil })
			return started, err
		}
		return s.workingSetRead(ctx, q, emit)
	})
}
