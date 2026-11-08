package imagecodec

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestMemeSharpDifferential(t *testing.T) {
	path := os.Getenv("HE_IMAGE_LIBRARY")
	if path == "" {
		path = os.Getenv("HUMANENGINE_VIPS_LIBRARY")
	}
	if path == "" {
		t.Skip("actual pinned native library required")
	}
	c, e := New(path)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("testdata/meme-sharp.json")
	if e != nil {
		t.Fatal(e)
	}
	var oracle struct {
		Cases []struct {
			Name, Input           string
			Width, Height, Frames int
			PNG, WebP             string
			Page                  int
		}
	}
	if e = json.Unmarshal(raw, &oracle); e != nil {
		t.Fatal(e)
	}
	for _, tc := range oracle.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			b, e := base64.StdEncoding.DecodeString(tc.Input)
			if e != nil {
				t.Fatal(e)
			}
			m, e := c.MemeMetadata(context.Background(), b)
			if e != nil {
				t.Fatal(e)
			}
			if m.Width != tc.Width || m.Height != tc.Height || m.Frames != tc.Frames {
				t.Fatalf("metadata %#v expected %d %d %d", m, tc.Width, tc.Height, tc.Frames)
			}
			png, e := c.MemeFramePNG(context.Background(), b, tc.Page)
			if e != nil {
				t.Fatal(e)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(png)); got != tc.PNG {
				t.Errorf("PNG %s expected %s", got, tc.PNG)
			}
			webp, e := c.MemeThumbnailWebP(context.Background(), b)
			if e != nil {
				t.Fatal(e)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(webp)); got != tc.WebP {
				t.Errorf("WebP %s expected %s", got, tc.WebP)
			}
		})
	}
}
