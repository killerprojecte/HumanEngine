// Package onebotimage handles native OneBot image locators shared by the two
// host transports. Network and retained message authority belong to the caller.
package onebotimage

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

var ErrUnavailable = errors.New("onebot_image_unavailable")

func FilePrefix(sceneID string, reference string) string {
	raw, ok := strings.CutPrefix(reference, "ob:media:")
	if !ok || len(sceneID) > 4096 {
		return ""
	}
	prefix := `{"sceneId":"` + sceneID + `","type":"image","file":"`
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
		return ""
	}
	return string(wire[len(prefix):n])
}

func InlinePayload(file string) (string, bool) {
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

func Descriptor(sceneID string, reference string) (*jsjson.Object, error) {
	raw, ok := strings.CutPrefix(reference, "ob:media:")
	if !ok {
		return nil, ErrUnavailable
	}
	wire, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrUnavailable
	}
	value, err := jsjson.ParseBytes(wire)
	media, ok := value.(*jsjson.Object)
	if err != nil || !ok {
		return nil, ErrUnavailable
	}
	bound, _ := media.Get("sceneId")
	kind, _ := media.Get("type")
	if bound != sceneID || kind != "image" {
		return nil, ErrUnavailable
	}
	return media, nil
}

func DecodeInline(file string) ([]byte, error) {
	payload, inline := InlinePayload(file)
	if !inline {
		return nil, ErrUnavailable
	}
	return DecodePayload(payload)
}

func DecodePayload(payload string) ([]byte, error) {
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

func OpaqueToken(media *jsjson.Object, file string) string {
	// Direct locators are never opaque tokens; their transport is independent.
	if hasFoldPrefix(file, "http:") || hasFoldPrefix(file, "https:") || hasFoldPrefix(file, "base64://") || hasFoldPrefix(file, "data:") {
		return ""
	}
	value, _ := media.Get("fileId")
	token, present := value.(string)
	if !present {
		token = file
	}
	if !ValidOpaqueToken(token) {
		return ""
	}
	return token
}

func ValidOpaqueToken(token string) bool {
	if token == "" || token == "." || token == ".." || strings.ContainsAny(token, "/\\:%\x00") {
		return false
	}
	units, err := jsjson.UTF16Len(token)
	return err == nil && units <= 16384
}

func hasFoldPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix)
}
