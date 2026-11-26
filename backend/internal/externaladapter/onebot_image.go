package externaladapter

import (
	"context"
	"encoding/json"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/onebotimage"
)

func classifyOneBotImage(scene botruntime.Scene, asset string) botruntime.NativeImageLink {
	file := onebotimage.FilePrefix(scene.ID, asset)
	if payload, ok := onebotimage.InlinePayload(file); ok && len(payload) != 0 && payload[0] != '"' {
		return botruntime.NativeImageInline
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
	token := onebotimage.OpaqueToken(media, text(media, "file"))
	if token == "" {
		return nil
	}
	return func(ctx context.Context, before func(context.Context) error) ([]byte, error) {
		return s.lookupOneBotImage(ctx, token, before)
	}
}

func (s *httpSession) readOneBotImage(ctx context.Context, scene botruntime.Scene, asset string, before func(context.Context) error) ([]byte, error) {
	if read := s.prepareOneBotImage(scene, asset); read != nil {
		return read(ctx, before)
	}
	return nil, botruntime.ErrDisplayImageUnavailable
}

// Only a retained, scoped opaque token may reach the configured peer. A
// locator supplied in its response is never used as a URL or local file.
func (s *httpSession) lookupOneBotImage(ctx context.Context, token string, before func(context.Context) error) ([]byte, error) {
	done, ok := s.work.Start()
	if !ok {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	defer done()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	detach := context.AfterFunc(s.life, cancel)
	defer detach()
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
	image, err := onebotimage.DecodeInline(text(selection, "source"))
	if err == onebotimage.ErrUnavailable {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	return image, err
}
