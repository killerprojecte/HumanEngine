package plugins

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableSnowInlineReceiveWorkingSet(t *testing.T) {
	ctx := context.Background()
	artifact := filepath.Join(t.TempDir(), "snowluma-http.wasm")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-buildmode=c-shared", "-ldflags=-s -w", "-o", artifact, "../../plugins/snowluma/portable-http")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=wasip1", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("WASM build: %v %s", err, out)
	}
	code, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewWASMHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(ctx)
	const id = "snowluma"
	if err := host.Enable(ctx, id, code); err != nil {
		t.Fatal(err)
	}
	// The adapter normalizes the inline locator without decoding the photo.
	inline := strings.Repeat("AbC0/a+1", 48*1024)
	raw := []byte(`{"operation":"adapterIngress","input":{"adapter":"snowluma","account":"42","event":{"post_type":"message","message_type":"group","self_id":42,"group_id":7,"user_id":8,"message_id":9,"time":1795499999,"message":[{"type":"text","data":{"text":"caption"}},{"type":"image","data":{"base64":"` + inline + `"}}]}}}`)
	var first []byte
	for i := 0; i < 140; i++ {
		out, err := host.Call(ctx, id, raw)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if i == 0 {
			first = out
			var result struct {
				Parts []struct{ Type, Text, AssetID string }
			}
			if err := json.Unmarshal(out, &result); err != nil || len(result.Parts) != 2 ||
				result.Parts[0].Type != "text" || result.Parts[0].Text != "caption" || result.Parts[1].Type != "media" {
				t.Fatalf("captioned image response mismatch: %v", err)
			}
			asset := strings.TrimPrefix(result.Parts[1].AssetID, "ob:media:")
			wire, err := base64.RawURLEncoding.DecodeString(asset)
			if err != nil || !bytes.Contains(wire, []byte(`"file":"base64://`+inline+`"`)) {
				t.Fatalf("inline locator changed: %v", err)
			}
		} else if !bytes.Equal(out, first) {
			t.Fatalf("projection changed at call %d", i)
		}
	}
	mib := host.instances[id].module.Memory().Size() / (1 << 20)
	t.Logf("guest linear memory after 140 inline events: %d MiB", mib)
	if mib >= 64 {
		t.Fatalf("guest working set grew past the regression bound: %d MiB", mib)
	}
}
