package snowlumaruntime

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
)

// OriginalReader is the engine's scoped IO/authority port, not a base64 fallback.
// Each read opens and releases its own native operation root. Keeping the returned
// descriptor does not pin a file, hold a read lock or defer deletion/revocation.
func OriginalReader(engine *memeengine.Service) func(context.Context, string, string) (*jsjson.Object, error) {
	return func(ctx context.Context, assetID, sceneID string) (*jsjson.Object, error) {
		if engine == nil || engine.Memes == nil {
			return nil, errors.New("snowluma_media_owner_missing")
		}
		root, release, err := engine.RootContext(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		original, err := engine.Memes.Original(root, memeservice.Context{SceneID: sceneID, Simulation: false}, assetID)
		if err != nil {
			return nil, err
		}
		return obj("path", original.Path, "contentType", original.ContentType, "bytes", original.Bytes, "hash", original.Hash), nil
	}
}
func (s *Sender) resolveOriginal(ctx context.Context, assetID any, scene *jsjson.Object) (*Source, error) {
	selected, err := s.call(obj("operation", "engine_source_select", "assetId", assetID))
	if err != nil {
		return nil, err
	}
	if str(selected, "kind") == "none" {
		return nil, nil
	}
	if str(selected, "kind") != "original" {
		return nil, errors.New("invalid_guest_source_selection")
	}
	asset, ok := get(selected, "assetId").(string)
	if !ok {
		return nil, errors.New("invalid_original_identifier")
	}

	sceneID := str(scene, "id")
	original, err := s.ports.ReadOriginal(ctx, asset, sceneID)
	if err != nil {
		return nil, err
	}
	value, err := s.call(obj("operation", "engine_source_value", "original", original))
	if err != nil {
		return nil, err
	}
	source, ok := get(value, "source").(*jsjson.Object)
	if !ok {
		return nil, errors.New("invalid_guest_source_value")
	}
	return &Source{Value: source, Validate: func() error {
		// Re-open authority only at the existing source beforeTransmit stage. A
		// logical release never grants access to a stale original descriptor.
		current, err := s.ports.ReadOriginal(ctx, asset, sceneID)
		if err != nil {
			return err
		}
		result, err := s.call(obj("operation", "engine_source_validate", "original", original, "current", current))
		if err != nil {
			return err
		}
		if str(result, "kind") != "valid" {
			return errors.New("outbound_media_changed")
		}
		return nil
	}}, nil
}
