package providertransport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
)

type reviewFixture struct {
	Roundtrip []struct {
		Protocol, AssistantArgumentJSON string
		Response, SecondRequest         map[string]any
	}
	Custom          []struct{ Name, Raw, ArgumentJSON, Error string }
	CustomRoundtrip struct{ AssistantArgumentJSON, SecondArgumentJSON string }
	URLs            []struct{ Suffix, Path, Error string }
}

func readReview(t *testing.T) reviewFixture {
	t.Helper()
	raw, e := os.ReadFile("testdata/review-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var f reviewFixture
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	return f
}
func reviewTools() []map[string]any {
	return []map[string]any{{"name": "lookup", "description": "Synthetic", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"z": map[string]any{"type": "number"}, "a": map[string]any{"type": "number"}}}}}
}
func reviewPolicy(t *testing.T, protocol string) *modelservice.Policy {
	t.Helper()
	zero, timeout := float64(0), float64(30000)
	stream := false
	p, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: protocol, RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text", "tools"}, Streaming: &stream}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func argumentJSON(t *testing.T, completion *Completion) string {
	t.Helper()
	call := completion.ToolCalls[0].(map[string]any)
	raw, e := providerwire.EncodeJSON(call["arguments"])
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}
func TestActualSourceCompleteToolOrderRoundtrip(t *testing.T) {
	for _, row := range readReview(t).Roundtrip {
		t.Run(row.Protocol, func(t *testing.T) {
			requests := make(chan map[string]any, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
					t.Error(e)
				}
				requests <- body
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(row.Response)
			}))
			defer server.Close()
			client, provider := nativeTestClient(server)
			provider.ID = "p"
			provider.Protocol = row.Protocol
			m := &NativeModels{Policy: reviewPolicy(t, row.Protocol), Client: client, Providers: map[string]Provider{"p": provider}}
			first, e := m.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{{Visible: map[string]any{"role": "user", "content": "synthetic"}}}, Tools: reviewTools()})
			if e != nil {
				t.Fatal(e)
			}
			<-requests
			if got := argumentJSON(t, first); got != row.AssistantArgumentJSON {
				t.Fatalf("assistant argument order %s != %s", got, row.AssistantArgumentJSON)
			}
			copied := *first.Assistant
			_, e = m.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{&copied}, Tools: reviewTools()})
			if e != nil {
				t.Fatal(e)
			}
			if body := <-requests; !reflect.DeepEqual(body, row.SecondRequest) {
				t.Fatalf("second actual request %#v != source %#v", body, row.SecondRequest)
			}
		})
	}
}
func TestActualSourceCustomArgumentValidation(t *testing.T) {
	for _, row := range readReview(t).Custom {
		t.Run(row.Name, func(t *testing.T) {
			result, e := parseCustom([]byte(row.Raw), reviewTools())
			if row.Error != "" {
				if e == nil || e.Error() != row.Error {
					t.Fatalf("error %v != %s", e, row.Error)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			calls := result["toolCalls"].([]any)
			raw, e := providerwire.EncodeJSON(calls[0].(map[string]any)["arguments"])
			if e != nil || string(raw) != row.ArgumentJSON {
				t.Fatalf("arguments %s %v != %s", raw, e, row.ArgumentJSON)
			}
		})
	}
}
func TestActualSourceWHATWGRequestURL(t *testing.T) {
	for _, row := range readReview(t).URLs {
		t.Run(row.Suffix, func(t *testing.T) {
			paths := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.RequestURI
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":[]}`)
			}))
			defer server.Close()
			client, provider := nativeTestClient(server)
			provider.BaseURL += row.Suffix
			_, e := client.Request(context.Background(), Request{Provider: provider, Path: "models"})
			if row.Error != "" {
				if e == nil || e.Error() != row.Error {
					t.Fatalf("error %v != %s", e, row.Error)
				}
				select {
				case path := <-paths:
					t.Fatalf("invalid URL transmitted %s", path)
				default:
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if path := <-paths; path != row.Path {
				t.Fatalf("actual request URI %q != source %q", path, row.Path)
			}
		})
	}
}
func TestActualSourceV1CompleteToolOrderRoundtrip(t *testing.T) {
	f := readReview(t)
	host := plugins.NewV1Host("node", io.Discard)
	defer host.Close(context.Background())
	path := filepath.Join(t.TempDir(), "order.mjs")
	source := `export default{manifest:{id:'fixture',configSchema:{type:'object'},capabilities:['model-protocol']},setup(ctx){ctx.registerModelProtocol('custom','Synthetic',{complete(context,messages){return{text:messages[0].role==='assistant'?JSON.stringify(messages[0].toolCalls[0].arguments):'',toolCalls:[{id:'c',name:'lookup',arguments:{z:1,a:2}}],usage:{inputTokens:1,outputTokens:1,totalTokens:2}}}})}}`
	if e := os.WriteFile(path, []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := host.Enable(context.Background(), "fixture", path, map[string]any{}); e != nil {
		t.Fatal(e)
	}
	protocol := "fixture:custom"
	m := &NativeModels{Host: host, Policy: reviewPolicy(t, protocol), Client: &Client{Credentials: testCredentials("synthetic"), Budgets: ExplicitOff{}}, Providers: map[string]Provider{"p": {ID: "p", Protocol: protocol, BaseURL: "https://synthetic.invalid"}}, ProviderSnapshots: map[string]map[string]any{"p": {"id": "p", "protocol": protocol, "baseUrl": "https://synthetic.invalid"}}, ModelSnapshots: map[string]map[string]any{"m": {"id": "m", "providerId": "p", "apiModelName": "synthetic"}}}
	first, e := m.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{{Visible: map[string]any{"role": "user", "content": "synthetic"}}}, Tools: reviewTools()})
	if e != nil {
		t.Fatal(e)
	}
	defer first.Release()
	if got := argumentJSON(t, first); got != f.CustomRoundtrip.AssistantArgumentJSON {
		t.Fatalf("first %s", got)
	}
	second, e := m.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{first.Assistant}, Tools: reviewTools()})
	if e != nil {
		t.Fatal(e)
	}
	defer second.Release()
	if second.Text != f.CustomRoundtrip.SecondArgumentJSON {
		t.Fatalf("V1 second %s != source %s", second.Text, f.CustomRoundtrip.SecondArgumentJSON)
	}
}
