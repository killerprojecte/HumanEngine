package providertransport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

func TestNativeResponsesContextAndIdentity(t *testing.T) {
	requests := make(chan map[string]any, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"reasoning","encrypted_content":"synthetic-private"},{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()
	client, provider := nativeTestClient(server)
	provider.Protocol = "openai-responses"
	provider.ID = "p"
	zero, timeout := float64(0), float64(1000)
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: provider.Protocol, RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text"}}}, Routes: []modelservice.Route{{Role: "replyer", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	models := &NativeModels{Policy: policy, Client: client, Providers: map[string]Provider{"p": provider}}
	first, e := models.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{{Visible: map[string]any{"role": "user", "content": "hello"}}}})
	if e != nil {
		t.Fatal(e)
	}
	<-requests
	contextMessage := &Message{Visible: map[string]any{"role": "user", "contextLayout": "messages", "content": `{"timezone":"UTC","messages":[{"text":"synthetic"}],"request":"reply"}`}}
	_, e = models.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{first.Assistant, contextMessage}})
	if e != nil {
		t.Fatal(e)
	}
	body := <-requests
	input := body["input"].([]any)
	if input[0].(map[string]any)["role"] != "system" || input[1].(map[string]any)["type"] != "reasoning" || len(input) != 6 {
		t.Fatalf("context/opaque sequence %#v", input)
	}
	copy := *first.Assistant
	_, e = models.Complete(context.Background(), CompletionRequest{Role: "replyer", Messages: []*Message{&copy}})
	if e != nil {
		t.Fatal(e)
	}
	body = <-requests
	input = body["input"].([]any)
	if len(input) != 1 || input[0].(map[string]any)["role"] != "assistant" {
		t.Fatalf("copied assistant acquired opaque state %#v", input)
	}
}
