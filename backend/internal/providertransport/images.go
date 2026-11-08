package providertransport

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
	"regexp"
	"strings"
)

const maxImageSourceBytes = 64 * 1024 * 1024

type ImageUploadLimits struct{ MaxImageBytes, MaxWidth, MaxHeight, MaxRequestBytes *int }

// ImageCompressor is a required codec boundary only when source policy demands
// decoding/compression. It must preserve sharp's metadata, animation and alpha
// semantics. The JSON envelope owner never substitutes a different codec.
type ImageCompressor interface {
	Compress(context.Context, []byte, ImageUploadLimits, bool) (string, error)
}
type NativeImageSerializer struct{ Compressor ImageCompressor }
type imageSlot struct {
	object map[string]any
	key    string
}

func (s imageSlot) get() string  { v, _ := s.object[s.key].(string); return v }
func (s imageSlot) set(v string) { s.object[s.key] = v }
func imageSlots(body map[string]any) []imageSlot {
	messages, ok := imageArray(body["messages"])
	if !ok {
		messages, _ = imageArray(body["input"])
	}
	slots := []imageSlot{}
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		parts, _ := imageArray(message["content"])
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if part["type"] == "input_image" {
				if _, ok := part["image_url"].(string); ok {
					slots = append(slots, imageSlot{part, "image_url"})
				}
			}
			if part["type"] == "image_url" {
				if image, ok := part["image_url"].(map[string]any); ok {
					if _, ok := image["url"].(string); ok {
						slots = append(slots, imageSlot{image, "url"})
					}
				}
			}
		}
	}
	return slots
}

var imageDataPattern = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,([A-Za-z0-9+/]*={0,2})$`)

func decodeImageData(value string) ([]byte, error) {
	match := imageDataPattern.FindStringSubmatch(value)
	if match == nil || match[2] == "" || len(match[2])%4 != 0 || len(match[2]) > ((maxImageSourceBytes+2)/3)*4 {
		return nil, errors.New("invalid_image_data")
	}
	data, e := base64.StdEncoding.DecodeString(match[2])
	if e != nil {
		return nil, errors.New("invalid_image_data")
	}
	return data, nil
}
func (n NativeImageSerializer) Serialize(ctx context.Context, body map[string]any, configured *ImageUploadLimits, requestLimit *int) ([]byte, error) {
	encoded, e := providerwire.EncodeJSON(body)
	if e != nil {
		return nil, e
	}
	copy, e := providerwire.DecodeObject(encoded)
	if e != nil {
		return nil, e
	}
	imageBytes, width, height := 10*1024*1024, 1536, 1536
	limits := ImageUploadLimits{MaxImageBytes: &imageBytes, MaxWidth: &width, MaxHeight: &height}
	if configured != nil {
		if configured.MaxImageBytes != nil {
			limits.MaxImageBytes = configured.MaxImageBytes
		}
		if configured.MaxWidth != nil {
			limits.MaxWidth = configured.MaxWidth
		}
		if configured.MaxHeight != nil {
			limits.MaxHeight = configured.MaxHeight
		}
		limits.MaxRequestBytes = configured.MaxRequestBytes
	}
	if requestLimit != nil {
		limits.MaxRequestBytes = requestLimit
	}
	inline := []imageSlot{}
	for _, slot := range imageSlots(copy) {
		if strings.HasPrefix(slot.get(), "data:") {
			inline = append(inline, slot)
		}
	}
	compress := func(value string, limits ImageUploadLimits) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		data, e := decodeImageData(value)
		if e != nil {
			return "", e
		}
		if n.Compressor == nil {
			return "", errors.New("native_image_codec_required")
		}
		return n.Compressor.Compress(ctx, data, limits, false)
	}
	for _, slot := range inline {
		if configured != nil || requestLimit != nil || len(slot.get()) > ((*limits.MaxImageBytes+2)/3)*4+32 {
			value, e := compress(slot.get(), limits)
			if e != nil {
				return nil, e
			}
			slot.set(value)
		}
	}
	serialized, e := providerwire.EncodeJSON(copy)
	if e != nil {
		return nil, e
	}
	if limits.MaxRequestBytes != nil {
		max := *limits.MaxRequestBytes
		if max < 1 {
			return nil, errors.New("invalid_image_upload_limits")
		}
		if len(serialized) > max && len(inline) > 0 {
			originals := make([]string, len(inline))
			for i, slot := range inline {
				originals[i] = slot.get()
				slot.set("")
			}
			fixed, e := providerwire.EncodeJSON(copy)
			if e != nil {
				return nil, e
			}
			perImage := (max - len(fixed)) / len(inline) // Source uses floor even for negative quantities.
			if max-len(fixed) < 0 && (max-len(fixed))%len(inline) != 0 {
				perImage--
			}
			target := (perImage - 23) / 4
			if perImage-23 < 0 && (perImage-23)%4 != 0 {
				target--
			}
			target *= 3
			if target > *limits.MaxImageBytes {
				target = *limits.MaxImageBytes
			}
			if target < 1 {
				return nil, errors.New("image_request_size_limit")
			}
			adapted := limits
			adapted.MaxImageBytes = &target
			for i, slot := range inline {
				value, e := compress(originals[i], adapted)
				if e != nil {
					return nil, e
				}
				slot.set(value)
			}
			serialized, e = providerwire.EncodeJSON(copy)
			if e != nil {
				return nil, e
			}
		}
		if len(serialized) > max {
			return nil, errors.New("image_request_size_limit")
		}
	}
	return serialized, nil
}

func imageArray(v any) ([]any, bool) {
	switch a := v.(type) {
	case []any:
		return a, true
	case []map[string]any:
		r := make([]any, len(a))
		for i, v := range a {
			r[i] = v
		}
		return r, true
	}
	return nil, false
}
