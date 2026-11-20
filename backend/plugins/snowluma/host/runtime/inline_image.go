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

// linkInlineImage recognizes the pinned guest's canonical image descriptor
// prefix. It reads only enough of the outer base64 to identify the file scheme;
// the direct read owns alphabet, size, integrity, and image-type validation.
func linkInlineImage(scene botruntime.Scene, reference string) bool {
	raw, ok := strings.CutPrefix(reference, "ob:media:")
	if !ok || len(scene.ID) > 4096 {
		return false
	}
	prefix := `{"sceneId":"` + scene.ID + `","type":"image","file":"`
	want := len(prefix) + len("data:image/jpeg;base64,") + 1
	encoded := min(len(raw), (want+2)/3*4)
	var small [512]byte
	wire := small[:]
	max := base64.RawURLEncoding.DecodedLen(encoded)
	if max > len(wire) {
		wire = make([]byte, max)
	}
	n, err := base64.RawURLEncoding.Decode(wire, []byte(raw[:encoded]))
	if err != nil || n <= len(prefix) || string(wire[:len(prefix)]) != prefix {
		return false
	}
	payload, ok := inlineImagePayload(string(wire[len(prefix):n]))
	return ok && len(payload) > 0 && payload[0] != '"'
}

func inlineImagePayload(file string) (string, bool) {
	if payload, ok := strings.CutPrefix(file, "base64://"); ok {
		return payload, true
	}
	header, encoded, found := strings.Cut(file, ",")
	if found && (strings.EqualFold(header, "data:image/png;base64") || strings.EqualFold(header, "data:image/jpeg;base64") ||
		strings.EqualFold(header, "data:image/gif;base64") || strings.EqualFold(header, "data:image/webp;base64")) {
		return encoded, true
	}
	return "", false
}

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
	payload, inline := inlineImagePayload(str(media, "file"))
	if !inline {
		return nil, botruntime.ErrDisplayImageUnavailable
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
