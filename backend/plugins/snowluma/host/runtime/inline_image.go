package snowlumaruntime

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

// readInlineImage accepts only a normalized native guest asset for its canonical
// scene. A URL or platform token is not a complete inline source and is not read.
func readInlineImage(ctx context.Context, scene botruntime.Scene, reference string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, ok := strings.CutPrefix(reference, "ob:media:")
	if !ok || scene.ID != snow.SceneID(scene.AdapterID, scene.AccountID, scene.Kind, scene.PlatformSceneID) {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	wire, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	value, err := jsjson.ParseBytes(wire)
	media, ok := value.(*jsjson.Object)
	if err != nil || !ok || str(media, "sceneId") != scene.ID || str(media, "type") != "image" {
		return nil, botruntime.ErrDisplayImageUnavailable
	}
	file := str(media, "file")
	payload, inline := strings.CutPrefix(file, "base64://")
	if !inline {
		header, encoded, found := strings.Cut(file, ",")
		if !found {
			return nil, botruntime.ErrDisplayImageUnavailable
		}
		if !strings.EqualFold(header, "data:image/png;base64") && !strings.EqualFold(header, "data:image/jpeg;base64") &&
			!strings.EqualFold(header, "data:image/gif;base64") && !strings.EqualFold(header, "data:image/webp;base64") {
			return nil, botruntime.ErrDisplayImageUnavailable
		}
		payload = encoded
	}
	const max = 64 * 1024 * 1024
	if len(payload) > (max+2)/3*4 {
		return nil, errors.New("media_size_limit")
	}
	// Go base64 would accept CR/LF, whereas the TypeScript source accepts no
	// whitespace. Valid nonzero trailing pad bits are accepted by both.
	if payload == "" || len(payload)%4 != 0 || strings.ContainsAny(payload, "\r\n") {
		return nil, errors.New("invalid_image_data")
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, errors.New("invalid_image_data")
	}
	if len(decoded) > max {
		return nil, errors.New("media_size_limit")
	}
	return decoded, nil
}
