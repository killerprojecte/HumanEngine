package memeengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
)

func (s *Service) admitQuotedManaged(ctx context.Context, turn *botruntime.TurnContext, candidate *nativeVisionCandidate) (bool, error) {
	scene := memeservice.Context{SceneID: turn.Scene.ID}
	id := candidate.source.AssetID()
	var original memeservice.Original
	var label string
	var err error
	if err := s.withNativeVisionRead(ctx, turn, candidate, func(reads botruntime.ReadAuthority) error {
		candidate.ticket, err = s.TurnRead.CapturePersonalizationScoped(reads, turn.Scene.ID)
		if err != nil || candidate.ticket == nil {
			return err
		}
		var asset *Object
		original, asset, err = s.Memes.AnalyzedOriginalInRuntimeScope(s.Sources, reads, scene, id)
		if asset != nil {
			label = MemeObservation(asset)
		}
		return err
	}); err != nil || label == "" {
		return false, err
	}
	if !verifiedManagedOriginal(original) {
		return false, nil
	}

	validateSource := candidate.validate
	candidate.validate = func(reads botruntime.ReadAuthority) (bool, error) {
		valid, err := validateSource(reads)
		if err != nil || !valid {
			return valid, err
		}
		valid, err = s.TurnRead.ValidatePersonalizationScoped(reads, candidate.ticket)
		if err != nil || !valid {
			return valid, err
		}
		current, asset, err := s.Memes.AnalyzedOriginalInRuntimeScope(s.Sources, reads, scene, id)
		return asset != nil && current == original && MemeObservation(asset) == label &&
			s.Memes.OriginalCurrent(id, original), err
	}
	if err := candidate.require(ctx); err != nil {
		return false, err
	}
	candidate.stored = label
	return true, nil
}

// Stored-label reuse needs proof of the original, not a copy of its pixels.
func verifiedManagedOriginal(original memeservice.Original) bool {
	file, err := os.Open(original.Path)
	if err != nil {
		return false
	}
	defer file.Close()
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(file, maxManagedOriginalBytes+1))
	return err == nil && n <= maxManagedOriginalBytes && float64(n) == original.Bytes &&
		hex.EncodeToString(digest.Sum(nil)) == original.Hash
}
