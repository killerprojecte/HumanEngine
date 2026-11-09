//go:build bundled_wasm

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
)

func TestDistributedArtifactsLoadWithoutSources(t *testing.T) {
	ctx := context.Background()
	host, err := NewWASMHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(ctx)
	for _, id := range []string{"typing-delay", "web-read", "web-search", "snowluma"} {
		bytes, err := BuiltinArtifact(id)
		if err != nil {
			t.Fatal(err)
		}
		if err = host.Enable(ctx, id, bytes); err != nil {
			t.Fatal(err)
		}
		result, err := host.Call(ctx, id, []byte(`{"operation":"manifest"}`))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			ID         string
			APIVersion int
		}
		if err = json.Unmarshal(result, &manifest); err != nil || manifest.ID != id || manifest.APIVersion != 2 {
			t.Fatal(id, string(result), err)
		}
		if err = host.Disable(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnknownBuiltinArtifactFilesystemError(t *testing.T) {
	_, err := BuiltinArtifact("missing")
	var pathError *fs.PathError
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pathError) || pathError.Op != "open" || pathError.Path != "builtins/missing.wasm" {
		t.Fatalf("filesystem error shape changed: %v", err)
	}
	first, err := BuiltinArtifact("snowluma")
	if err != nil {
		t.Fatal(err)
	}
	original := first[0]
	first[0] ^= 255
	second, err := BuiltinArtifact("snowluma")
	if err != nil || second[0] != original {
		t.Fatal("artifact data was shared with caller", err)
	}
}
