package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/learningowner"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"golang.org/x/text/encoding/unicode"
)

func (s *Server) WithEmbeddingIndexActions(owner *learningowner.Owner, p *recovery.ReadPublisher, builder func(context.Context, *learningowner.Object, map[string]any) (*providertransport.NativeModels, error)) error {
	if s.embeddingIndexRead == nil || !p.OwnedBy(s.main, s.authority) {
		return errors.New("embedding_index_action_publication_missing")
	}
	op, err := owner.BindIndexAdminAction(builder)
	if err == nil {
		s.embeddingIndexAction = op
		s.embeddingActionPublisher = p
	}
	return err
}

func embeddingIndexActionPath(method, path string) (action string, matched bool) {
	tail, ok := strings.CutPrefix(path, embeddingIndexPath+"/")
	if method != http.MethodPost || !ok || tail == "" || strings.Contains(tail, "/") {
		return "", false
	}
	return habitTryDecode(tail, false), true
}

func parseEmbeddingIndexAction(action string, r *http.Request) (*learningowner.IndexAction, error) {
	switch action {
	case "start", "pause", "cancel", "resume":
	default:
		return nil, nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	contentType := r.Header.Get("Content-Type")
	if contentType == "" && len(raw) == 0 {
		return nil, nil
	}
	if !habitJSONContentType.MatchString(contentType) {
		return nil, errors.New("embedding_action_unsupported_media_type")
	}
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
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
	input, ok := value.(*jsjson.Object)
	if !ok {
		return nil, nil
	}
	out := &learningowner.IndexAction{Action: action}
	expected := false
	for _, member := range input.Members() {
		switch member.Key {
		case "expectedJobId":
			expected = true
			if member.Value != nil {
				id, ok := member.Value.(string)
				if !ok {
					return nil, nil
				}
				out.ExpectedJobID = &id
			}
		case "expectedEncoderRevision":
			revision, ok := member.Value.(string)
			if !ok {
				return nil, nil
			}
			out.ExpectedEncoderRevision = revision
		case "confirmUnknownRequest":
			confirmed, ok := member.Value.(bool)
			if !ok {
				return nil, nil
			}
			out.ConfirmUnknownRequest = confirmed
		default:
			return nil, nil
		}
	}
	if !expected {
		return nil, nil
	}
	return out, nil
}

func (s *Server) serveEmbeddingIndexAction(w http.ResponseWriter, r *http.Request, available bool, requestID, path string) {
	action, matched := embeddingIndexActionPath(r.Method, path)
	if !matched && available {
		ctx := r.Context()
		response := newPublicationResponse(w.Header(), r.Method == http.MethodHead)
		response.status = http.StatusNotFound
		response.header.Set("Content-Type", "text/plain; charset=UTF-8")
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		err := s.embeddingActionPublisher.Publish(ctx, func(*sql.Conn) ([]byte, error) { return []byte("404 Not Found"), nil },
			func(body []byte) error { return response.enqueue(ctx, body) })
		if response.accepted {
			_ = response.drain(ctx, w)
			return
		}
		if err != nil {
			available = false
		}
	}

	s.serveMemoryScopedRead(w, r, available, requestID, "embedding_index_action.failed", func(emit func(int, []byte) error) (bool, error) {
		ctx := r.Context()
		publish := func(code int, body string) error {
			return s.embeddingActionPublisher.Publish(ctx, func(*sql.Conn) ([]byte, error) { return []byte(body), nil },
				func(body []byte) error { return emit(code, body) })
		}
		if err := s.embeddingActionPublisher.Publish(ctx, func(*sql.Conn) ([]byte, error) { return nil, nil }, func([]byte) error { return nil }); err != nil {
			return false, err
		}
		input, err := parseEmbeddingIndexAction(action, r)
		if err != nil {
			return true, err
		}
		if input == nil {
			return true, publish(http.StatusBadRequest, `{"error":"Invalid request"}`)
		}
		if input.Action == "start" && input.ExpectedEncoderRevision == "" {
			return true, publish(http.StatusBadRequest, `{"error":"embedding_index_refresh_required"}`)
		}
		if err = s.embeddingIndexAction(ctx, *input); err == nil {
			accepted := false
			_, err = s.embeddingIndexRead(ctx, func(body []byte) error {
				e := emit(http.StatusOK, body)
				accepted = e == nil
				return e
			})
			if err == nil || accepted {
				return true, err
			}
		}
		// Never replay an accepted job when status materialization fails.
		return true, publish(http.StatusBadRequest, `{"error":"embedding_index_action_unavailable"}`)
	})
}
