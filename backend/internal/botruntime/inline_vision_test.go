package botruntime

import "testing"

func TestInlineVisionSkipsOrdinaryPartsBeforeRuntimeLookup(t *testing.T) {
	// No runtime owner: an ordinary or unmanaged part must never reach lookup.
	turn := &TurnContext{}
	turn.Scene.ID = "scene"
	message := obj("id", "message", "sceneId", "scene", "origin", "live", "status", "available", "senderId", "user", "parts", []any{
		obj("type", "text", "text", "ordinary"),
		obj("type", "media", "mediaType", "image/png", "assetId", "managed-other-owner"),
		obj("type", "media", "mediaType", "audio", "assetId", "ob:media:audio"),
	})
	if image, _ := turn.InlineVisionSource(message); image != nil {
		t.Fatal("ordinary parts cannot select native vision input")
	}
}
