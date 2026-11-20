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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	media, err := nativeImageDescriptor(scene, reference)
	if err != nil {
		return nil, err
	}
	file := str(media, "file")
	if publicImageLocator(file) {
		return s.readPublicBrowserImage(ctx, file, before)
	}
	return readTokenOrInlineDescriptor(ctx, media, file, func(call context.Context, token string) (any, error) {
		return s.lookupImageToken(call, token, before)
	})
}

func (s *Sender) lookupImageToken(call context.Context, token string, before func(context.Context) error) (any, error) {
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
}

func readTokenOrInlineImage(ctx context.Context, scene botruntime.Scene, reference string, lookup imageTokenLookup) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	media, err := nativeImageDescriptor(scene, reference)
	if err != nil {
		return nil, err
	}
	return readTokenOrInlineDescriptor(ctx, media, str(media, "file"), lookup)
}

func readTokenOrInlineDescriptor(ctx context.Context, media *jsjson.Object, file string, lookup imageTokenLookup) ([]byte, error) {
	if payload, inline := inlineImagePayload(file); inline {
		return decodeInlinePayload(payload)
	}
	token := opaqueImageToken(media, file)
	if token == "" {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	return fetchInlineImageToken(ctx, token, lookup)
}

func fetchInlineImageToken(ctx context.Context, token string, lookup imageTokenLookup) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
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

func (s *Sender) prepareImageToken(scene botruntime.Scene, reference string) botruntime.GuardedImageRead {
	// Canonical direct locators cannot refresh. Inspect only the prefix so a
	// malformed/unsupported inline payload doesn't need a full descriptor read.
	if directImageLocator(imageFilePrefix(scene, reference)) {
		return nil
	}
	media, err := nativeImageDescriptor(scene, reference)
	if err != nil {
		return nil
	}
	token := opaqueImageToken(media, str(media, "file"))
	if token == "" {
		return nil
	}
	return func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
		return fetchInlineImageToken(ctx, token, func(call context.Context, token string) (any, error) {
			return s.lookupImageToken(call, token, before)
		})
	}
}

func opaqueImageToken(media *jsjson.Object, file string) string {
	// Direct locators are never opaque tokens. The browser owns direct HTTP(S);
	// model images still accept only inline bytes and supported opaque tokens.
	if directImageLocator(file) {
		return ""
	}
	token, present := get(media, "fileId").(string)
	if !present {
		token = file
	}
	if token == "" || token == "." || token == ".." || strings.ContainsAny(token, "/\\:%\x00") {
		return ""
	}
	if units, err := jsjson.UTF16Len(token); err != nil || units > 16384 {
		return ""
	}
	return token
}

func publicImageLocator(value string) bool {
	return hasFoldPrefix(value, "http:") || hasFoldPrefix(value, "https:")
}

func directImageLocator(value string) bool {
	return publicImageLocator(value) || hasFoldPrefix(value, "base64://") || hasFoldPrefix(value, "data:")
}

func hasFoldPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix)
}
