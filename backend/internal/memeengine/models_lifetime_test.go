package memeengine

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type lifetimeCredentials struct{}

func (lifetimeCredentials) Resolve(context.Context, providertransport.Provider) (string, error) {
	return "synthetic-key", nil
}
func lifetimeNativeModels(t *testing.T) *providertransport.NativeModels {
	t.Helper()
	host := plugins.NewV1Host("node", io.Discard)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	path := filepath.Join(t.TempDir(), "fixture.mjs")
	source := `export default{manifest:{id:'fixture',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){const value={toJSON(){throw Error('opaque serialized')}};value.self=value;ctx.registerModelProtocol('custom','Fixture',{complete(context,messages,tools,previous){return{text:previous===value?'continued':'fresh',toolCalls:[],usage:{inputTokens:1,outputTokens:1,totalTokens:2},continuation:value}}})}}`
	if e := os.WriteFile(path, []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := host.Enable(context.Background(), "fixture", path, map[string]any{}); e != nil {
		t.Fatal(e)
	}
	zero, timeout := float64(0), float64(1000)
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "fixture:custom", RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	return &providertransport.NativeModels{Policy: policy, Client: &providertransport.Client{Credentials: lifetimeCredentials{}, Budgets: providertransport.ExplicitOff{}}, Providers: map[string]providertransport.Provider{"p": {ID: "p", Protocol: "fixture:custom", BaseURL: "https://synthetic.invalid"}}, Host: host, ProviderSnapshots: map[string]map[string]any{"p": {"id": "p", "protocol": "fixture:custom", "baseUrl": "https://synthetic.invalid"}}, ModelSnapshots: map[string]map[string]any{"m": {"id": "m", "providerId": "p", "apiModelName": "synthetic"}}}
}
func TestRuntimeContinuationReleaseActualV1(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "normal"
		if late {
			name = "late"
		}
		t.Run(name, func(t *testing.T) {
			models := lifetimeNativeModels(t)
			ctx := context.Background()
			first, e := models.Complete(ctx, providertransport.CompletionRequest{Role: "replyer", Messages: []*providertransport.Message{{Visible: map[string]any{"role": "user", "content": "synthetic"}}}})
			if e != nil || first.Text != "fresh" {
				t.Fatal(e)
			}
			state := &continuationState{messages: map[*Object]*providertransport.Message{}}
			if late {
				if e = state.ReleaseTurnState(); e != nil {
					t.Fatal(e)
				}
			}
			kept, e := state.retain(fields("role", "assistant"), first.Assistant)
			if e != nil || kept == late {
				t.Fatal(kept, e)
			}
			if !late {
				if e = state.ReleaseTurnState(); e != nil {
					t.Fatal(e)
				}
			}
			if e = state.ReleaseTurnState(); e != nil {
				t.Fatal(e)
			}
			next, e := models.Complete(ctx, providertransport.CompletionRequest{Role: "replyer", Messages: []*providertransport.Message{first.Assistant}})
			if e != nil {
				t.Fatal(e)
			}
			defer next.Release()
			if next.Text != "fresh" {
				t.Fatal("released turn continuation retained in worker", next.Text)
			}
		})
	}
}
