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
	current := func(call context.Context, ws *recovery.WriteSession) (bool, error) {
		if !s.Read.ValidInSession(call, ticket, ws) {
			return false, nil
		}
		var valid bool
		err := s.Sources.WithSession(ws).WithRuntimeReads(call, func(reads botruntime.ReadAuthority) error {
			var err error
			valid, err = part.Validate(reads)
			return err
		})
		return valid && part.Current(), err
	}
	asset, err := s.Memes.IntakeRetainedKnown(ctx, c, b, source, s.Codec, func(ws *recovery.WriteSession) error {
		valid, err := current(ctx, ws)
		if err != nil {
			return err
		}
		if !valid {
			return botruntime.ErrDisplayImageUnavailable
		}
		return nil
	})
	if err != nil || part.InlineObservationLearningEnabled() || get(asset, "analyzed") == true || get(asset, "knownOnly") != true || get(asset, "shared") == true {
		return err
	}
	ctx, release, err := s.RootContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.Memes.AnalyzeRetainedSticker(ctx, c, asset, source, s.Models, s.Codec, func(call context.Context, ws *recovery.WriteSession) (valid bool, err error) {
		if !s.Read.ValidInSession(call, ticket, ws) {
			return false, nil
		}
		err = s.Sources.WithSession(ws).WithRuntimeReads(call, func(reads botruntime.ReadAuthority) error {
			var err error
			valid, err = s.currentInlineVision(reads, scene, part)
			return err
		})
		return valid, err
	})
}
