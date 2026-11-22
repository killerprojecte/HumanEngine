package snowlumaruntime

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type imageTokenLookup func(context.Context, string) (any, error)
type selectedImageRead func(context.Context, string) ([]byte, error)

func (s *Sender) readBrowserImage(ctx context.Context, scene botruntime.Scene, reference string, before func(context.Context) error) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	media, err := nativeImageDescriptor(scene, reference)
	if err != nil {
		return nil, err
	}
	file := str(media, "file")
	lookup := func(call context.Context, token string) (any, error) {
		return s.lookupImageToken(call, token, before)
	}
	public := func(call context.Context, locator string) ([]byte, error) {
		return s.readPublicBrowserImage(call, locator, before)
	}
	if publicImageLocator(file) {
		return readPublicImageWithTokenRefresh(ctx, media, file, lookup, public)
	}
	return readTokenOrInlineDescriptor(ctx, media, file, lookup, browserTokenContinuation(publicBrowserImageSupported, public))
}

// A direct public 403 is the scoped stale-locator signal. All other failures
// retain the original result and cannot authorize a second platform action.
func readPublicImageWithTokenRefresh(ctx context.Context, media *jsjson.Object, file string, lookup imageTokenLookup, public selectedImageRead) ([]byte, error) {
	image, err := public(ctx, file)
	if err != publicImageHTTPStatus(http.StatusForbidden) || ctx.Err() != nil {
		return image, err
	}
	token := str(media, "fileId")
	// Token syntax excludes the colon in the original HTTP(S) URL.
	if !validOpaqueToken(token) {
		return image, err
	}
	return fetchImageToken(ctx, token, lookup, browserTokenContinuation(true, public))
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
	return readTokenOrInlineDescriptor(ctx, media, str(media, "file"), lookup, inlineTokenContinuation)
}

func readTokenOrInlineDescriptor(ctx context.Context, media *jsjson.Object, file string, lookup imageTokenLookup, readSelected selectedImageRead) ([]byte, error) {
	if payload, inline := inlineImagePayload(file); inline {
		return decodeInlinePayload(payload)
	}
	token := opaqueImageToken(media, file)
	if token == "" {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	return fetchImageToken(ctx, token, lookup, readSelected)
}

func fetchImageToken(ctx context.Context, token string, lookup imageTokenLookup, readSelected selectedImageRead) ([]byte, error) {
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
	// A nonempty URL owns the result, even if its read later fails.
	return readSelected(ctx, redirect)
}

func browserTokenContinuation(supported bool, readPublic selectedImageRead) selectedImageRead {
	if !supported {
		return inlineTokenContinuation
	}
	return func(ctx context.Context, source string) ([]byte, error) {
		if publicImageLocator(source) {
			return readPublic(ctx, source)
		}
		return inlineTokenContinuation(ctx, source)
	}
}

func inlineTokenContinuation(_ context.Context, source string) ([]byte, error) {
	image, err := decodeInlineImage(source)
	if errors.Is(err, botruntime.ErrDisplayImageUnavailable) {
		return nil, errors.New("image_download_url_unavailable")
	}
	return image, err
}

func (s *Sender) prepareVisionImage(scene botruntime.Scene, reference string) botruntime.GuardedImageRead {
	media, err := nativeImageDescriptor(scene, reference)
	if err != nil {
		return nil
	}
	file := str(media, "file")
	if publicImageLocator(file) {
		// URL-only originals share the guarded browser transport, not its
		// expired-locator refresh. A second locator does not qualify here.
		id, present := media.Get("fileId")
		if !publicBrowserImageSupported || (present && id != "") {
			return nil
		}
		return func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
			return s.readPublicBrowserImage(ctx, file, before)
		}
	}
	token := opaqueImageToken(media, file)
	if token == "" {
		return nil
	}
	return func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
		return fetchImageToken(ctx, token, func(call context.Context, token string) (any, error) {
			return s.lookupImageToken(call, token, before)
		}, inlineTokenContinuation)
	}
}

func opaqueImageToken(media *jsjson.Object, file string) string {
	// Direct locators are never opaque tokens; their transport is independent.
	if directImageLocator(file) {
		return ""
	}
	token, present := get(media, "fileId").(string)
	if !present {
		token = file
	}
	if !validOpaqueToken(token) {
		return ""
	}
	return token
}

func validOpaqueToken(token string) bool {
	if token == "" || token == "." || token == ".." || strings.ContainsAny(token, "/\\:%\x00") {
		return false
	}
	units, err := jsjson.UTF16Len(token)
	return err == nil && units <= 16384
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
