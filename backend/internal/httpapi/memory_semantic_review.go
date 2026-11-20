package httpapi

import (
	"bytes"
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

func memorySemanticReviewRoute(method, path string) (string, bool) {
	if method != http.MethodPost {
		return "", false
	}
	tail, ok := strings.CutPrefix(path, memorySemanticsPath+"/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(tail, "/review")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return habitTryDecode(id, false), true
}

func semanticReviewQuery(r *http.Request, id string) (*memoryservice.SemanticReviewQuery, error) {
	if workspaceID(id) == "" {
		return nil, nil
	}
	media := r.Header.Get("Content-Type")
	if media != "" && !habitJSONContentType.MatchString(media) {
		return nil, errors.New("semantic_review_unsupported_media")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if media == "" {
		if len(raw) == 0 {
			return nil, nil
		}
		return nil, errors.New("semantic_review_unsupported_media")
	}
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
	if !ok || obj.Len() != 4 {
		return nil, nil
	}
	scene, _ := obj.Get("sceneId")
	versionValue, _ := obj.Get("expectedVersion")
	version, _ := versionValue.(float64)
	statusValue, _ := obj.Get("status")
	status, _ := statusValue.(string)
	confirmed, _ := obj.Get("confirmed")
	sceneID := workspaceID(scene)
	if sceneID == "" || version < 1 || version > 9007199254740991 || math.Trunc(version) != version ||
		(status != "admitted" && status != "rejected") || confirmed != true {
		return nil, nil
	}
	return &memoryservice.SemanticReviewQuery{ID: id, SceneID: sceneID, Version: version, Status: status}, nil
}

func (s *Server) serveMemorySemanticReview(w http.ResponseWriter, r *http.Request, available bool, requestID, id string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_semantics_review.failed", func(emit func(int, []byte) error) (bool, error) {
		query, err := semanticReviewQuery(r, id)
		return s.memoryAdminRead.ReviewSemantic(r.Context(), query, err, s.schemas, emit)
	})
}
