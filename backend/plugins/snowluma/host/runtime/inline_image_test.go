package snowlumaruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
)

func TestInlineImageActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/inline-image-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scene botruntime.Scene
		Cases []struct{ Name, Asset, Bytes, Error string }
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if linked := linkInlineImage(fixture.Scene, c.Asset); linked != (c.Name != "empty") {
				t.Fatalf("inline link classification = %t", linked)
			}
			image, err := readInlineImage(context.Background(), fixture.Scene, c.Asset)
			if c.Error != "" {
				if err == nil || err.Error() != c.Error {
					t.Fatalf("decoder error = %v, want %q", err, c.Error)
				}
			} else if err != nil || base64.StdEncoding.EncodeToString(image) != c.Bytes {
				t.Fatalf("decoder bytes differ, length=%d error=%v", len(image), err)
			}
		})
	}
}

func TestInlineImageLinkReadsOnlyCanonicalPrefix(t *testing.T) {
	scene := botruntime.Scene{}
	scene.ID = "ob:s:fixture"
	asset := func(sceneID, mediaType, file string) string {
		wire, err := json.Marshal(struct {
			Scene string `json:"sceneId"`
			Type  string `json:"type"`
			File  string `json:"file"`
		}{sceneID, mediaType, file})
		if err != nil {
			t.Fatal(err)
		}
		return "ob:media:" + base64.RawURLEncoding.EncodeToString(wire)
	}
	for _, candidate := range []string{
		asset(scene.ID, "image", "https://fixture.invalid/base64://YQ=="),
		asset(scene.ID, "image", "platform-file-token"),
		asset(scene.ID, "image", "data:image/svg+xml;base64,YQ=="),
		asset(scene.ID, "audio", "base64://YQ=="),
		asset("ob:s:other", "image", "base64://YQ=="),
	} {
		if linkInlineImage(scene, candidate) {
			t.Fatal("noncanonical inline image got a link")
		}
	}
	large := asset(scene.ID, "image", "base64://"+strings.Repeat("YQ==", 4096))
	if !linkInlineImage(scene, large[:len(large)-1]+"!") {
		t.Fatal("classification scanned the tail; only the authorized fetch owns full validation")
	}
}
