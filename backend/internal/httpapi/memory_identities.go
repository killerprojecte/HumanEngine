package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/identityprivacy"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"golang.org/x/text/encoding/unicode"
)

func (s *Server) serveMemoryIdentities(w http.ResponseWriter, r *http.Request, available bool, requestID string) {
	s.serveMemoryScopedRead(w, r, available, requestID, "memory_identities.failed", func(emit func(int, []byte) error) (bool, error) {
		if r.Method == http.MethodPost {
			input, err := identityCommand(r)
			if err != nil {
				return s.memoryAdminRead.RejectIdentityBody(r.Context(), err)
			}
			return s.memoryAdminRead.ChangeIdentity(r.Context(), input, emit)
		}
		return s.memoryAdminRead.PublishIdentities(r.Context(), func(body []byte) error {
			return emit(http.StatusOK, body)
		})
	})
}

func identityCommand(r *http.Request) (*identityprivacy.Command, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	contentType := r.Header.Get("Content-Type")
	if contentType == "" && len(raw) == 0 {
		// Hono validates an absent untyped body as an empty object.
		return nil, nil
	}
	if !habitJSONContentType.MatchString(contentType) {
		return nil, errors.New("identity_unsupported_media")
	}
	// Fetch's JSON body decoding removes one initial UTF-8 BOM.
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
	return identityprivacy.ParseCommand(value), nil
}
