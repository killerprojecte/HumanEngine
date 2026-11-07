//go:build bundled_wasm

package plugins

import (
	"context"
	"encoding/json"
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
