package providertransport

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

func v1Models(t *testing.T) (*NativeModels, *plugins.V1Host, string) {
	t.Helper()
	host := plugins.NewV1Host("node", io.Discard)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	path := filepath.Join(t.TempDir(), "synthetic.mjs")
	source := `export default{manifest:{id:'fixture',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){const value={private:'synthetic',toJSON(){throw Error('opaque serialized')}};value.self=value;ctx.registerModelProtocol('custom','Fixture',{complete(context,messages,tools,previous){if(context.credential!=='synthetic-key')throw Error('credential missing');return{text:previous===value?'continued':'fresh',toolCalls:[],usage:{inputTokens:1,outputTokens:2,totalTokens:3},continuation:value}}})}}`
	if e := os.WriteFile(path, []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := host.Enable(context.Background(), "fixture", path, map[string]any{}); e != nil {
		t.Fatal(e)
	}
	retry, timeout := float64(0), float64(1000)
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "fixture:custom", RetryCount: &retry, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	return &NativeModels{Policy: policy, Client: &Client{Credentials: testCredentials("synthetic-key"), Budgets: ExplicitOff{}}, Providers: map[string]Provider{"p": {ID: "p", Protocol: "fixture:custom", BaseURL: "https://synthetic.invalid"}}, Host: host, ProviderSnapshots: map[string]map[string]any{"p": {"id": "p", "protocol": "fixture:custom", "baseUrl": "https://synthetic.invalid"}}, ModelSnapshots: map[string]map[string]any{"m": {"id": "m", "providerId": "p", "apiModelName": "synthetic"}}}, host, path
}
func TestV1CompletionExactAssistantAndGeneration(t *testing.T) {
	models, host, path := v1Models(t)
	ctx := context.Background()
	first, e := models.Complete(ctx, CompletionRequest{Role: "replyer", Messages: []*Message{{Visible: map[string]any{"role": "user", "content": "synthetic"}}}, Tools: []map[string]any{}})
	if e != nil || first.Text != "fresh" {
		t.Fatalf("first %#v %v", first, e)
	}
	defer first.Assistant.Release()
	continued, e := models.Complete(ctx, CompletionRequest{Role: "replyer", Messages: []*Message{first.Assistant}, Tools: []map[string]any{}})
	if e != nil || continued.Text != "continued" {
		t.Fatalf("continuation %#v %v", continued, e)
	}
	defer continued.Assistant.Release()
	copied := *first.Assistant
	fresh, e := models.Complete(ctx, CompletionRequest{Role: "replyer", Messages: []*Message{&copied}, Tools: []map[string]any{}})
	if e != nil || fresh.Text != "fresh" {
		t.Fatalf("copy retained ownership %#v %v", fresh, e)
	}
	defer fresh.Assistant.Release()
	if e = host.Disable(ctx, "fixture"); e != nil {
		t.Fatal(e)
	}
	if _, e = host.Enable(ctx, "fixture", path, map[string]any{}); e != nil {
		t.Fatal(e)
	}
	replacement, e := models.Complete(ctx, CompletionRequest{Role: "replyer", Messages: []*Message{first.Assistant}, Tools: []map[string]any{}})
	if e != nil || replacement.Text != "fresh" {
		t.Fatalf("stale generation %#v %v", replacement, e)
	}
	defer replacement.Assistant.Release()
}
func TestCustomValidationBeforeOpaqueClaim(t *testing.T) {
	for _, raw := range []string{`{"text":"x","toolCalls":[],"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":3}}`, `{"text":"x","toolCalls":[{"id":"c","name":"unknown","arguments":{}}],"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`} {
		if _, e := parseCustom([]byte(raw), nil); e == nil {
			t.Fatal("invalid result accepted")
		}
	}
}
