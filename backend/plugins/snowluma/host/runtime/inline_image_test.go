package snowlumaruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
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
			if linked := (classifyRetainedImage(fixture.Scene, c.Asset) == botruntime.NativeImageInline); linked != (c.Name != "empty") {
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
	direct := asset(scene.ID, "image", "https://fixture.invalid/base64://YQ==")
	var wantDirect botruntime.NativeImageLink
	if publicBrowserImageSupported {
		wantDirect = botruntime.NativeImageBrowserDirect
	}
	if got := classifyRetainedImage(scene, direct); got != wantDirect {
		t.Fatalf("HTTPS locator containing inline-like text: got %d, want %d", got, wantDirect)
	}
	for _, candidate := range []string{
		asset(scene.ID, "image", "data:image/svg+xml;base64,YQ=="),
		asset(scene.ID, "audio", "base64://YQ=="),
		asset("ob:s:other", "image", "base64://YQ=="),
	} {
		if classifyRetainedImage(scene, candidate) != 0 {
			t.Fatal("noncanonical inline image got a link")
		}
	}
	native := botruntime.Scene{SceneRef: dispatch.SceneRef{AdapterID: "snowluma", AccountID: "42", Kind: "group", PlatformSceneID: "99"}}
	native.ID = snow.SceneID(native.AdapterID, native.AccountID, native.Kind, native.PlatformSceneID)
	if got := classifyRetainedImage(native, asset(native.ID, "image", "platform-file-token")); got != botruntime.NativeImageBrowserOpaque {
		t.Fatalf("canonical opaque token class = %d", got)
	}
	large := asset(scene.ID, "image", "base64://"+strings.Repeat("YQ==", 4096))
	if classifyRetainedImage(scene, large[:len(large)-1]+"!") != botruntime.NativeImageInline {
		t.Fatal("classification scanned the tail; only the authorized fetch owns full validation")
	}
}

func TestTokenImageActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/inline-image-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scene      botruntime.Scene
		TokenCases []struct {
			Name, Asset, Bytes, Error, GoLimited string
			Reply                                json.RawMessage
			Actions                              []struct{ Action, File string }
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.TokenCases) != 9 {
		t.Fatalf("missing token cases: %v", err)
	}
	for _, c := range fixture.TokenCases {
		t.Run(c.Name, func(t *testing.T) {
			wantOpaque := c.GoLimited != "no-refresh" && c.GoLimited != "no-path-token"
			if opaque := (new(Sender).prepareImageToken(fixture.Scene, c.Asset) != nil); opaque != wantOpaque {
				t.Fatalf("vision token eligibility = %t, want %t", opaque, wantOpaque)
			}
			lookups := 0
			value, err := jsjson.ParseBytes(c.Reply)
			if err != nil {
				t.Fatal(err)
			}
			got, err := readTokenOrInlineImage(context.Background(), fixture.Scene, c.Asset, func(_ context.Context, token string) (any, error) {
				lookups++
				if len(c.Actions) != 1 || c.Actions[0].Action != "get_image" || c.Actions[0].File != token {
					t.Fatalf("unexpected token action: %q", token)
				}
				return value, nil
			})
			wantLookups := len(c.Actions)
			switch c.GoLimited {
			case "no-refresh", "no-path-token":
				wantLookups = 0
				if !errors.Is(err, botruntime.ErrDisplayImageUnavailable) {
					t.Fatalf("source outside the opaque-token subset was admitted: %v", err)
				}
			case "no-public-fetch":
				if err == nil || err.Error() != "image_download_url_unavailable" {
					t.Fatalf("external reply did not stay unavailable: %v", err)
				}
			case "":
				if c.Error != "" {
					if err == nil || err.Error() != c.Error {
						t.Fatalf("error = %v, want %q", err, c.Error)
					}
				} else if err != nil || base64.StdEncoding.EncodeToString(got) != c.Bytes {
					t.Fatalf("token payload differs: len=%d err=%v", len(got), err)
				}
			default:
				t.Fatalf("unknown source subset expectation %q", c.GoLimited)
			}
			if lookups != wantLookups {
				t.Fatalf("lookups = %d, want %d", lookups, wantLookups)
			}
		})
	}
}

func TestOpaqueBrowserLinkUsesSelectedNativeToken(t *testing.T) {
	scene := botruntime.Scene{SceneRef: dispatch.SceneRef{AdapterID: "snowluma", AccountID: "42", Kind: "group", PlatformSceneID: "99"}}
	scene.ID = snow.SceneID(scene.AdapterID, scene.AccountID, scene.Kind, scene.PlatformSceneID)
	const opaque = botruntime.NativeImageBrowserOpaque
	for _, fixture := range []struct {
		name, file string
		explicit   any
		want       botruntime.NativeImageLink
	}{
		{"file", "token", nil, opaque},
		{"explicit-beats-path", "/local/file", "valid-platform-token", opaque},
		{"path-is-not-token", "/local/file", nil, 0},
		{"explicit-empty-no-fallback", "valid-platform-token", "", 0},
		{"explicit-path-no-fallback", "valid-platform-token", "/local/file", 0},
		{"malformed-direct-does-not-fallback", "data:image/svg+xml;base64,YQ==", "explicit", 0},
		{"at-js-length", strings.Repeat("😀", 8192), nil, opaque},
		{"over-js-length", strings.Repeat("😀", 8193), nil, 0},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			media := obj("sceneId", scene.ID, "type", "image", "file", fixture.file)
			if fixture.explicit != nil {
				_ = media.Set("fileId", fixture.explicit)
			}
			wire, err := jsjson.Marshal(media)
			if err != nil {
				t.Fatal(err)
			}
			asset := "ob:media:" + base64.RawURLEncoding.EncodeToString(wire)
			if got := classifyRetainedImage(scene, asset); got != fixture.want {
				t.Fatalf("class=%d want %d", got, fixture.want)
			}
		})
	}
}
