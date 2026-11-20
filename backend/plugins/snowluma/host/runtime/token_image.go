package snowlumaruntime

import (
	"context"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type imageTokenLookup func(context.Context, string) (any, error)

func (s *Sender) readBrowserImage(ctx context.Context, scene botruntime.Scene, reference string, before func(context.Context) error) ([]byte, error) {
	return readTokenOrInlineImage(ctx, scene, reference, func(call context.Context, token string) (any, error) {
		done, ok := s.work.Start()
		if !ok {
			return nil, botruntime.ErrDisplayImageUnavailable
		}
		defer done()
		if s.current() != nil {
			return nil, botruntime.ErrDisplayImageUnavailable
		}
		var denial error
		qualify := func(ctx context.Context) error {
			denial = before(ctx)
			return denial
		}
		params := obj("file", token)
		var value any
		var err error
		if s.http != nil {
			value, err = s.http.ActionWithGuard(call, "get_image", params, qualify)
		} else {
			value, err = s.ws.ActionWithGuard(call, "get_image", params, func() error { return qualify(call) })
		}
		if denial != nil {
			return nil, denial
		}
		return value, err
	})
}

func readTokenOrInlineImage(ctx context.Context, scene botruntime.Scene, reference string, lookup imageTokenLookup) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	media, err := nativeImageDescriptor(scene, reference)
	if err != nil {
		return nil, err
	}
	file := str(media, "file")
	if payload, inline := inlineImagePayload(file); inline {
		return decodeInlinePayload(payload)
	}
	// Public locators must observe source direct-first ordering. Until the
	// bounded public owner is composed, do not "refresh" them instead.
	if directImageLocator(file) {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	token, present := get(media, "fileId").(string)
	if !present {
		token = file
	}
	if token == "" || token == "." || token == ".." || strings.ContainsAny(token, "/\\:%\x00") {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	if units, err := jsjson.UTF16Len(token); err != nil || units > 16384 {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	value, err := lookup(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, ok := value.(*jsjson.Object)
	if !ok {
		return nil, errors.New("invalid_payload")
	}
	redirect := str(result, "url")
	if redirect == "" {
		redirect = str(result, "file")
	}
	image, err := decodeInlineImage(redirect)
	if errors.Is(err, botruntime.ErrDisplayImageUnavailable) {
		// Do not fall through to a file when the server returned a nonempty URL.
		return nil, errors.New("image_download_url_unavailable")
	}
	return image, err
}

func directImageLocator(value string) bool {
	for _, prefix := range [...]string{"http:", "https:", "base64://", "data:"} {
		if len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}
