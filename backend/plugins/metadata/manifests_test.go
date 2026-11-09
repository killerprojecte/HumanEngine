package metadata

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginOwnedManifestBytesAndIsolation(t *testing.T) {
	for _, id := range []string{"typing-delay", "web-read", "web-search", "snowluma"} {
		t.Run(id, func(t *testing.T) {
			expected, err := os.ReadFile(filepath.Join("..", id, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			raw, ok := Manifest(id).(json.RawMessage)
			if !ok || !bytes.Equal(raw, expected) {
				t.Fatal("manifest bytes or schema order changed")
			}
			raw[0] = '!'
			next, ok := Manifest(id).(json.RawMessage)
			if !ok || !bytes.Equal(next, expected) {
				t.Fatal("manifest resource was mutated by its caller")
			}
		})
	}
	if value := Manifest("missing").(map[string]any)["error"]; value != "builtin_manifest_missing" {
		t.Fatal(value)
	}
}
