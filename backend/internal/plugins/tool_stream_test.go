package plugins

import (
	"context"
	"encoding/json"
	"github.com/killerprojecte/HumanEngine/backend/internal/toolresults"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestToolCacheStreamingRealStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.mjs")
	source := `export default{manifest:{id:'stream',configSchema:{type:'object'},capabilities:[]},setup(ctx){ctx.registerTool({name:'stream.read',effect:'read',inputSchema:{type:'object'},execute:async(_,turn)=>turn.cacheResult((async function*(){yield new Uint8Array([65,0,66]);yield new Uint8Array([255,195,169])})(),{contentType:'application/octet-stream',previewBytes:2})})}}`
	if e := os.WriteFile(path, []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	h := NewV1Host("node", io.Discard)
	defer h.Close(context.Background())
	raw, e := h.Enable(context.Background(), "stream", path, map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	var ready struct{ Registrations []struct{ Handle string } }
	if e = json.Unmarshal(raw, &ready); e != nil {
		t.Fatal(e)
	}
	cache := toolresults.New(toolresults.Config{TempDir: t.TempDir()})
	defer cache.Dispose()
	result, e := h.CallTool(context.Background(), "stream", ready.Registrations[0].Handle, []any{map[string]any{}, map[string]any{}}, ToolServices{CacheResult: func(ctx context.Context, source toolresults.Source, options toolresults.Options) (toolresults.Result, error) {
		return cache.Store(ctx, source, toolresults.Owner{Scope: "scope", SceneID: "scene", PluginID: "stream"}, options)
	}})
	if e != nil {
		t.Fatal(e)
	}
	var view toolresults.Result
	if e = json.Unmarshal(result, &view); e != nil {
		t.Fatal(e)
	}
	if view.TotalBytes != 6 || view.Content != "QQA=" || view.NextOffset == nil || *view.NextOffset != 2 {
		t.Fatalf("preview %s", result)
	}
	full, e := cache.Read(context.Background(), toolresults.ReadInput{ResultID: view.ResultID, Full: true}, "scope", nil)
	if e != nil || full.Content != "QQBC/8Op" {
		t.Fatal(full, e)
	}
}
