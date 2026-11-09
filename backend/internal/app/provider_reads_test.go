package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestApplicationProviderReadsLiveConfigurationAndRegistry(t *testing.T) {
	options := applicationOptions(t)
	options.Entries = []string{"provider.mjs"}
	source := `export default{manifest:{id:'provider',configSchema:{type:'object'},capabilities:['model-protocol']},setup(c){c.registerModelProtocol('custom','Fixture',{complete(){return{text:'',toolCalls:[]}},listModels(){return['custom']}})}}`
	if e := os.WriteFile(filepath.Join(options.PluginRoot, "provider.mjs"), []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	app, e := Open(context.Background(), options)
	if e != nil {
		if app != nil {
			app.Shutdown(context.Background())
		}
		t.Fatal(e)
	}
	defer app.Shutdown(context.Background())
	if got := appRequest(t, app, "GET", "/api/model-protocols", nil).([]any); len(got) != 2 {
		t.Fatal(got)
	}
	if e = app.Plugins.Enable(context.Background(), "provider", ""); e != nil {
		t.Fatal(e)
	}
	if got := appRequest(t, app, "GET", "/api/model-protocols", nil).([]any); len(got) != 3 {
		t.Fatal(got)
	}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"native"}]}`)
	}))
	defer peer.Close()
	config := func(protocol string) map[string]any {
		return map[string]any{"providers": []any{map[string]any{"id": "p", "name": "Synthetic", "protocol": protocol, "baseUrl": peer.URL, "credentialEnv": "", "retryCount": 0, "timeoutMs": 1000}}, "models": []any{}, "routes": []any{}}
	}
	appRequest(t, app, "PUT", "/api/models", config("openai-chat"))
	got := appRequest(t, app, "GET", "/api/providers/p/models", nil)
	if field(got, "models").([]any)[0] != "native" {
		t.Fatal(got)
	}
	appRequest(t, app, "PUT", "/api/models", config("provider:custom"))
	got = appRequest(t, app, "GET", "/api/providers/p/models", nil)
	if field(got, "models").([]any)[0] != "custom" {
		t.Fatal(got)
	}
	if e = app.Plugins.Disable(context.Background(), "provider", ""); e != nil {
		t.Fatal(e)
	}
	if got := appRequest(t, app, "GET", "/api/model-protocols", nil).([]any); len(got) != 2 {
		t.Fatal(got)
	}
	if app.Bridge.Host.PID() != 0 {
		t.Fatal("last disabled plugin retained process")
	}
}
