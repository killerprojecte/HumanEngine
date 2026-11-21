package app

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
)

func (a *Application) observeNativeSticker(observation botruntime.NativeInlineObservation) {
	parts := observation.Select(memeengine.IsStickerMetadata)
	if len(parts) == 0 {
		return
	}
	scene, id := observation.Identity()
	_, _ = a.MediaTasks.Accept(scene, id, func(ctx context.Context) error {
		var first error
		for _, part := range parts {
			err := a.Engine.IntakeNativeSticker(ctx, part)
			if err == nil || ctx.Err() != nil || errors.Is(err, botruntime.ErrDisplayImageUnavailable) || !part.Current() {
				continue
			}
			if first == nil {
				first = err
				emit(a.options.Logs, observability.Event{Level: "warn", Module: "media", Code: "media.intake_unavailable", Message: "Native sticker intake failed", SceneID: scene})
			}
		}
		return first
	})
}
