//go:build bundled_wasm

package snowluma_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	snowluma "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma"
)

// This test binary can run in a directory containing no application sources or
// external wasm file. It binds the actual linked artifact, not a source fallback.
func TestBundledSnowPolicyArtifact(t *testing.T) {
	artifact, err := snowluma.Artifact()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	actual := hex.EncodeToString(digest[:])
	t.Logf("linked Snow guest SHA256 %s", actual)
	if expected := os.Getenv("EXPECTED_SNOW_GUEST_SHA256"); expected != "" && expected != actual {
		t.Fatalf("linked artifact mismatch: %s != %s", actual, expected)
	}
	host, err := plugins.NewWASMHost(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	if err = host.Enable(context.Background(), "snowluma", artifact); err != nil {
		t.Fatal(err)
	}
	call, err := host.BindCall("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ input, key, want string }{
		{`{"operation":"manifest"}`, "id", "snowluma"},
		{`{"operation":"configuration_validate","config":{}}`, "kind", "issues"},
	} {
		raw, err := call(context.Background(), []byte(check.input))
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err = json.Unmarshal(raw, &result); err != nil || result[check.key] != check.want {
			t.Fatalf("%s: %s %v", check.input, raw, err)
		}
	}
	if err = host.Disable(context.Background(), "snowluma"); err != nil {
		t.Fatal(err)
	}
	if _, err = call(context.Background(), []byte(`{"operation":"manifest"}`)); err == nil {
		t.Fatal("disabled artifact retained callable generation")
	}
}
