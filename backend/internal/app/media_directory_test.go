package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
)

func TestMediaDirectoryUsesSourceLocationAndRefusesLegacyOriginals(t *testing.T) {
	parent := t.TempDir()
	options := Options{Installation: installation.Options{Database: filepath.Join(parent, "humanengine.sqlite")}}
	media, legacy := filepath.Join(parent, "media"), filepath.Join(parent, "memes")
	assertCanonical := func() {
		t.Helper()
		if dir, err := options.mediaDirectory(); dir != media || err != nil {
			t.Fatalf("media directory = %q, %v; want %q", dir, err, media)
		}
	}
	assertCanonical() // Fresh installation.
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	assertCanonical() // Empty previous preview directory.
	legacyFile := filepath.Join(legacy, "old.png")
	if err := os.WriteFile(legacyFile, []byte("legacy original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, hasCanonical := range []bool{false, true} {
		if hasCanonical {
			if err := os.Mkdir(media, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if dir, err := options.mediaDirectory(); dir != "" || !errors.Is(err, ErrLegacyMediaRequiresMove) {
			t.Fatalf("legacy media directory = %q, %v", dir, err)
		}
	}
	if data, err := os.ReadFile(legacyFile); err != nil || string(data) != "legacy original" {
		t.Fatalf("legacy file changed: %v", err)
	}
	options.MemeDirectory = legacy
	if dir, err := options.mediaDirectory(); dir != legacy || err != nil {
		t.Fatalf("explicit directory = %q, %v; want %q", dir, err, legacy)
	}
}
