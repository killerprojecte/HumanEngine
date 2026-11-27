package externaladapter

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/onebotimage"
	"github.com/killerprojecte/HumanEngine/backend/internal/publicimage"
)

func (s *httpSession) classifyOneBotImage(scene botruntime.Scene, asset string) botruntime.NativeImageLink {
	file := onebotimage.FilePrefix(scene.ID, asset)
	if payload, ok := onebotimage.InlinePayload(file); ok && len(payload) != 0 && payload[0] != '"' {
		return botruntime.NativeImageInline
	}
	if s.publicImage != nil && publicimage.IsLocator(file) {
		return botruntime.NativeImageBrowserDirect
	}
	if file == "" {
		return 0
	}
	if media, err := onebotimage.Descriptor(scene.ID, asset); err == nil && onebotimage.OpaqueToken(media, text(media, "file")) != "" {
		return botruntime.NativeImageBrowserOpaque
	}
	return 0
}

func readOneBotInline(ctx context.Context, scene botruntime.Scene, asset string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	media, err := onebotimage.Descriptor(scene.ID, asset)
	if err != nil {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	body, err := onebotimage.DecodeInline(text(media, "file"))
	if err == onebotimage.ErrUnavailable {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	return body, err
}

func (s *httpSession) prepareOneBotImage(scene botruntime.Scene, asset string) botruntime.GuardedImageRead {
	media, err := onebotimage.Descriptor(scene.ID, asset)
	if err != nil {
		return nil
	}
	file := text(media, "file")
	if s.publicImage != nil && publicimage.IsLocator(file) {
		if id, present := media.Get("fileId"); present && id != "" {
			return nil
		}
		return s.guardImageRead(func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
			return s.publicImage.Read(ctx, file, before)
		})
	}
	if token := onebotimage.OpaqueToken(media, file); token != "" {
		return s.guardImageRead(func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
			return s.lookupOneBotImage(ctx, token, before, false)
		})
	}
	return nil
}

func (s *httpSession) readOneBotImage(ctx context.Context, scene botruntime.Scene, asset string, before func(context.Context) error) ([]byte, error) {
	media, err := onebotimage.Descriptor(scene.ID, asset)
	if err != nil {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	file := text(media, "file")
	if s.publicImage != nil && publicimage.IsLocator(file) {
		return s.guardImageRead(func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
			image, err := s.publicImage.Read(ctx, file, before)
			if err == publicimage.HTTPStatus(http.StatusForbidden) && ctx.Err() == nil {
				if token := text(media, "fileId"); onebotimage.ValidOpaqueToken(token) {
					return s.lookupOneBotImage(ctx, token, before, true)
				}
			}
			return image, err
		})(ctx, before)
	}
	if token := onebotimage.OpaqueToken(media, file); token != "" {
		return s.guardImageRead(func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
			return s.lookupOneBotImage(ctx, token, before, false)
		})(ctx, before)
	}
	return nil, botruntime.ErrDisplayImageUnavailable
}

func (s *httpSession) guardImageRead(read botruntime.GuardedImageRead) botruntime.GuardedImageRead {
	return func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
		done, ok := s.work.Start()
		if !ok {
			return nil, botruntime.ErrDisplayImageUnavailable
		}
		defer done()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		detach := context.AfterFunc(s.life, cancel)
		defer detach()
		return read(ctx, before)
	}
}

// Only a retained, scoped token may reach the authenticated peer. A browser
// refresh after a public 403 may follow another qualified public locator;
// token-only and vision reads still accept only inline bytes.
func (s *httpSession) lookupOneBotImage(ctx context.Context, token string, before func(context.Context) error, refresh bool) ([]byte, error) {
	payload, _ := json.Marshal(struct {
		File string `json:"file"`
	}{token})
	raw, err := s.oneBotRead(ctx, "get_image", payload, before)
	if err != nil {
		if err != errOneBotRead {
			return nil, err
		}
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	answer, err := s.invoke(ctx, "adapterImage", json.RawMessage(raw))
	if err != nil {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	selection, err := guestObject(answer)
	if err != nil {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	source := text(selection, "source")
	if refresh && publicimage.IsLocator(source) {
		return s.publicImage.Read(ctx, source, before)
	}
	image, err := onebotimage.DecodeInline(source)
	if err == onebotimage.ErrUnavailable {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	return image, err
}
