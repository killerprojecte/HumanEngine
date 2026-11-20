package snowlumaruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
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
