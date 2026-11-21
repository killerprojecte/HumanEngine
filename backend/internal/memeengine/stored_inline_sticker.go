package memeengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
)

func (s *Service) storedInlineSticker(ctx context.Context, turn *botruntime.TurnContext, source *botruntime.DisplayImageSource, bytes []byte, ticket *modelread.Ticket) (string, func(botruntime.ReadAuthority) (bool, error), error) {
	if ticket == nil {
		return "", nil, nil
	}
	digest := sha256.Sum256(bytes)
	hash := hex.EncodeToString(digest[:])
	c := memeservice.Context{SceneID: turn.Scene.ID}
	var asset *Object
	err := s.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		valid, err := s.currentInlineVision(reads, turn.Scene.ID, source)
		if err != nil || !valid {
			return err
		}
		valid, err = s.TurnRead.ValidatePersonalizationScoped(reads, ticket)
		if err != nil || !valid {
			return err
		}
		asset, err = s.Memes.AnalyzedContentInRuntimeScope(s.Sources, reads, c, hash, "")
		return err
	})
	if err != nil || asset == nil {
		return "", nil, err
	}
	id := str(get(asset, "id"))
	description := MemeObservation(asset)
	guard := func(reads botruntime.ReadAuthority) (bool, error) {
		valid, err := s.TurnRead.ValidatePersonalizationScoped(reads, ticket)
		if err != nil || !valid {
			return valid, err
		}
		current, err := s.Memes.AnalyzedContentInRuntimeScope(s.Sources, reads, c, hash, id)
		return current != nil && MemeObservation(current) == description, err
	}
	return description, guard, nil
}
