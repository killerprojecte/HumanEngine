package httpapi

import (
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestManagedPreviewURLKeepsOriginalPartIndex(t *testing.T) {
	row, err := jsjson.Parse(`{"id":"bot/message","sceneId":"fixture:scene","status":"available","origin":"live","parts":[
		{"type":"unsupported","description":"Untrusted platform image metadata: {\"kind\":\"image\"}"},
		{"type":"media","mediaType":"image","assetId":"` + strings.Repeat("c", 64) + `"},
		{"type":"unsupported","description":"Untrusted platform image metadata: {\"kind\":\"sticker\"}"},
		{"type":"media","mediaType":"image","assetId":"ob:media:unproven","mediaUrl":"https://untrusted.invalid/synthetic"}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	message := row.(*jsjson.Object)
	presentSceneMessage(message, true)
	raw, err := jsjson.Stringify(message)
	if err != nil {
		t.Fatal(err)
	}
	want := `"parts":[{"type":"media","mediaType":"image","mediaUrl":"/api/scenes/fixture%3Ascene/messages/bot%2Fmessage/media/1","mediaKind":"sticker"},{"type":"media","mediaType":"image","mediaKind":"image"}]`
	if !strings.Contains(raw, want) {
		t.Fatalf("projected media differs: %s", raw)
	}
}
