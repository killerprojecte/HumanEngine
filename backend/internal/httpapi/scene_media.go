package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

type sceneMediaRequest struct{ sceneID, messageID, index string }

func sceneMediaRoute(method, path string) (sceneMediaRequest, bool) {
	if method != "GET" && method != "HEAD" {
		return sceneMediaRequest{}, false
	}
	tail, ok := strings.CutPrefix(path, "/api/scenes/")
	if !ok {
		return sceneMediaRequest{}, false
	}
	sceneID, rest, ok := strings.Cut(tail, "/messages/")
	if !ok || sceneID == "" || strings.Contains(sceneID, "/") {
		return sceneMediaRequest{}, false
	}
	messageID, index, ok := strings.Cut(rest, "/media/")
	if !ok || messageID == "" || index == "" || strings.Contains(messageID, "/") || strings.Contains(index, "/") {
		return sceneMediaRequest{}, false
	}
	return sceneMediaRequest{habitTryDecode(sceneID, false), habitTryDecode(messageID, false), habitTryDecode(index, false)}, true
}

func (p sceneMediaRequest) partIndex() int {
	if len(p.index) > 3 || len(p.index) > 1 && p.index[0] == '0' {
		return -1
	}
	index := 0
	for i := range len(p.index) {
		digit := p.index[i] - '0'
		if digit > 9 {
			return -1
		}
		index = 10*index + int(digit)
	}
	scenePoints, sceneErr := jsjson.CodePointCount(p.sceneID)
	messagePoints, messageErr := jsjson.CodePointCount(p.messageID)
	if sceneErr != nil || messageErr != nil || scenePoints > 4096 || messagePoints > 8192 {
		return -1
	}
	return index
}

func (s *Server) serveSceneMedia(w http.ResponseWriter, r *http.Request, available bool, requestID string, p sceneMediaRequest) {
	head := r.Method == "HEAD"
	fail := func(status int, code string) {
		if r.Context().Err() != nil {
			return
		}
		if head {
			w.WriteHeader(status)
			return
		}
		habitError(w, status, code, "")
	}
	index := p.partIndex()
	if index < 0 {
		fail(http.StatusBadRequest, "Invalid request")
		return
	}
	if !available {
		fail(http.StatusServiceUnavailable, "image_fetch_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ctx, release, err := s.memes.RootContext(ctx)
	if err != nil {
		fail(http.StatusServiceUnavailable, "image_fetch_unavailable")
		return
	}
	defer release()
	image, err := s.memes.ReadManagedDisplayImage(ctx, p.sceneID, p.messageID, index)
	if err == nil {
		response := newPublicationResponse(w.Header(), head)
		response.header.Set("Content-Type", image.MIME)
		response.header.Set("Content-Length", strconv.Itoa(len(image.Bytes)))
		response.header.Set("Content-Disposition", "inline")
		response.header.Set("Cache-Control", "no-store")
		response.header.Set("X-Content-Type-Options", "nosniff")
		response.header.Set("Cross-Origin-Resource-Policy", "same-origin")
		err = s.runtimeStatus.publisher.Publish(ctx, func(conn *sql.Conn) ([]byte, error) {
			return image.Bytes, image.VerifyPublication(ctx, conn)
		}, func(body []byte) error { return response.enqueue(ctx, body) })
		if response.accepted {
			_ = response.drain(r.Context(), w)
			return
		}
	}
	if errors.Is(err, memeengine.ErrDisplayImageUnavailable) {
		fail(http.StatusNotFound, "image_unavailable")
	} else if ctx.Err() != nil {
		fail(http.StatusServiceUnavailable, "image_request_cancelled")
	} else {
		switch err.Error() {
		case "image_size_limit", "invalid_or_unsupported_image":
			fail(http.StatusUnsupportedMediaType, err.Error())
		default:
			_ = s.logs.Emit(observability.Event{Level: "error", Module: "http", Code: "scene_media.failed", Message: "Scene media read failed", RequestID: requestID})
			fail(http.StatusServiceUnavailable, "image_fetch_unavailable")
		}
	}
}
