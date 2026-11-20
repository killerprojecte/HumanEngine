package httpapi

import (
	"database/sql"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"golang.org/x/text/encoding/unicode"
)

func memoryRelationReviewPath(method, path string) (string, bool) {
	if method != http.MethodPost {
		return "", false
	}
	tail, ok := strings.CutPrefix(path, "/api/memory-relations/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(tail, "/review")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return habitTryDecode(id, false), true
}

func memoryRelationReviewQuery(id string, request *http.Request) (*memoryservice.RelationReviewQuery, error) {
	if memoryReadID(id) == "" {
		return nil, nil
	}
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	contentType := request.Header.Get("Content-Type")
	if contentType == "" && len(raw) == 0 {
		// The source OpenAPI declaration makes the body optional, passing an
		// empty object to the scene check instead of running the JSON validator.
		return &memoryservice.RelationReviewQuery{}, nil
	}
	if !habitJSONContentType.MatchString(contentType) {
		return nil, errors.New("relation_review_unsupported_media")
	}
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
	if !ok || obj.Len() != 3 {
		return nil, nil
	}
	sceneRaw, _ := obj.Get("sceneId")
	scene, _ := sceneRaw.(string)
	versionRaw, _ := obj.Get("expectedVersion")
	version, _ := versionRaw.(float64)
	statusRaw, _ := obj.Get("status")
	status, _ := statusRaw.(string)
	if memoryReadID(scene) == "" || version < 1 || version > 9007199254740991 ||
		math.Trunc(version) != version || (status != "accepted" && status != "rejected") {
		return nil, nil
	}
	return &memoryservice.RelationReviewQuery{ID: id, SceneID: scene, ExpectedVersion: version, Status: status}, nil
}

func (s *Server) serveMemoryRelationReview(w http.ResponseWriter, r *http.Request, available bool, requestID, relationID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_relation_review.failed", func(emit func(int, []byte) error) (bool, error) {
		ctx := r.Context()
		// The authenticated source checks recovery before parsing a request body.
		if err := s.authority.Publish(ctx, s.main, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil }); err != nil {
			return false, err
		}
		query, err := memoryRelationReviewQuery(relationID, r)
		if err != nil {
			return true, err
		}
		_, err = s.memoryAdminRead.ReviewAndPublishRelation(ctx, query, s.schemas, emit)
		return true, err
	})
}
