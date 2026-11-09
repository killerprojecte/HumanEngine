package snowluma

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// OriginalDecision preserves the Engine's SnowLuma source callback policy.
// Native code reads the already authorized original; no file bytes enter WASM.
func OriginalDecision(input *jsjson.Object) any {
	switch textProperty(input, "operation") {
	case "engine_source_select":
		rawAsset := property(input, "assetId")
		asset, err := jsString(rawAsset)
		if err != nil || len(asset) != 64 {
			return httpJSON{"kind": "none"}
		}
		for _, c := range []byte(asset) {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return httpJSON{"kind": "none"}
			}
		}
		return httpJSON{"kind": "original", "assetId": rawAsset}
	case "engine_source_value":
		original, err := object(property(input, "original"))
		if err != nil {
			return httpJSON{"kind": "error", "code": "send_failed"}
		}
		source := original.Clone()
		_ = source.Set("type", "image")
		return httpJSON{"kind": "source", "source": source}
	case "engine_source_validate":
		original, e := object(property(input, "original"))
		current, f := object(property(input, "current"))
		if e != nil || f != nil {
			return httpJSON{"kind": "error", "code": "outbound_media_changed"}
		}
		for _, key := range []string{"path", "hash", "bytes", "contentType"} {
			// Native Original records contain only string/number scalar fields.
			if property(original, key) != property(current, key) {
				return httpJSON{"kind": "error", "code": "outbound_media_changed"}
			}
		}
		return httpJSON{"kind": "valid"}
	}
	return httpJSON{"kind": "error", "code": "unknown_plugin_operation"}
}
