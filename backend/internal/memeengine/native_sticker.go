package memeengine

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func (s *Service) IntakeNativeSticker(ctx context.Context, part *botruntime.DisplayImageSource) error {
	scene, message, sender := part.InlineObservationIdentity()
	ticket, err := s.Read.Capture(ctx, scene)
	if err != nil || !ticket.PersonalizationEnabled() {
		return err
	}
	b, err := part.ReadInlineObservation(ctx)
	if err != nil {
		return err
	}
	source := fields("sceneId", scene, "messageId", message, "senderId", sender)
	evidence := fields("sceneId", scene, "messageId", message, "senderId", sender, "available", true)
	c := memeservice.Context{SceneID: scene, SourceEvidence: []*Object{evidence}}
	_, err = s.Memes.IntakeRetainedKnown(ctx, c, b, source, s.Codec, func(ws *recovery.WriteSession) error {
		if !s.Read.ValidInSession(ctx, ticket, ws) {
			return botruntime.ErrDisplayImageUnavailable
		}
		return s.Sources.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			valid, err := part.Validate(reads)
			if err != nil {
				return err
			}
			if !valid || !part.Current() {
				return botruntime.ErrDisplayImageUnavailable
			}
			return nil
		})
	})
	return err
}
