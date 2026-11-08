package learningbatch

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type syntheticCredential struct{}

func (syntheticCredential) Resolve(context.Context, providertransport.Provider) (string, error) {
	return "synthetic-key", nil
}
func syntheticModels(t *testing.T, respond func(map[string]any) map[string]any) *providertransport.NativeModels {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
			http.Error(w, "synthetic invalid", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respond(body))
	}))
	t.Cleanup(server.Close)
	zero, timeout := 0.0, 3000.0
	stream := false
	policy, e := modelservice.New(modelservice.Config{Providers: []modelservice.Provider{{ID: "p", Protocol: "openai-chat", RetryCount: &zero, TimeoutMS: &timeout}}, Models: []modelservice.Model{{ID: "m", ProviderID: "p", APIModelName: "synthetic", Capabilities: []string{"text", "tools"}, Streaming: &stream}}, Routes: []modelservice.Route{{Role: "learning", OrderedModelIDs: []string{"m"}}}}, modelservice.Options{})
	if e != nil {
		t.Fatal(e)
	}
	m := &providertransport.NativeModels{Policy: policy, Client: &providertransport.Client{HTTP: server.Client(), Credentials: syntheticCredential{}, Budgets: providertransport.ExplicitOff{}}, Providers: map[string]providertransport.Provider{"p": {ID: "p", Protocol: "openai-chat", BaseURL: server.URL}}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if e := m.CloseAndWait(ctx); e != nil {
			t.Error(e)
		}
	})
	return m
}
func responseCalls(calls ...map[string]any) map[string]any {
	a := []any{}
	for i, c := range calls {
		raw, _ := providerwire.EncodeJSON(c["arguments"])
		a = append(a, map[string]any{"id": fmt.Sprintf("call-%d", i), "type": "function", "function": map[string]any{"name": providerwire.WireName(c["name"].(string)), "arguments": string(raw)}})
	}
	reason := "tool_calls"
	if len(calls) == 0 {
		reason = "stop"
	}
	return map[string]any{"choices": []any{map[string]any{"finish_reason": reason, "message": map[string]any{"role": "assistant", "content": "", "tool_calls": a}}}}
}
func TestNativeTransportPreservesInvalidCallPositionAndStripsExtractorAuthority(t *testing.T) {
	b, _ := persistenceFixture(t)
	b.projection = replyplan.NewContextProjection("s")
	b.r.d.Models = syntheticModels(t, func(request map[string]any) map[string]any {
		messages := request["messages"].([]any)
		user := messages[len(messages)-1].(map[string]any)
		var payload map[string]any
		if e := json.Unmarshal([]byte(user["content"].(string)), &payload); e != nil {
			t.Error(e)
		}
		message := payload["messages"].([]any)[0].(map[string]any)
		id := message["id"]
		return responseCalls(map[string]any{"name": "learning.propose", "arguments": map[string]any{"content": "Invalid reference", "kind": "fact", "sourceMessageIds": []any{"message_unknown"}}}, map[string]any{"name": "learning.propose", "arguments": map[string]any{"content": "Valid sibling", "kind": "fact", "sourceMessageIds": []any{id}, "mergeIntoId": "memory_unknown", "supersedesId": "memory_unknown"}})
	})
	calls, extra, e := b.extract()
	if e != nil || extra || len(calls) != 2 {
		t.Fatalf("extract %v %v %v", calls, extra, e)
	}
	if get(calls[0].Arguments, "invalidReference") != true {
		t.Fatalf("invalid position: %+v", calls[0])
	}
	if get(calls[1].Arguments, "mergeIntoId") != nil || get(calls[1].Arguments, "supersedesId") != nil || !contains(arr(get(calls[1].Arguments, "sourceMessageIds")), "m") {
		t.Fatal("extractor acquired authority or lost sibling")
	}
}
func TestNativeTransportEmptyReviewUsesSingleSourceBudget(t *testing.T) {
	b, _ := persistenceFixture(t)
	b.projection = replyplan.NewContextProjection("s")
	_ = b.messages[0].Set("parts", []any{obj("type", "text", "text", "Correction: this is an explicit durable correction of the source.")})
	count := 0
	b.r.d.Models = syntheticModels(t, func(request map[string]any) map[string]any {
		count++
		if count == 1 {
			return responseCalls()
		}
		messages := request["messages"].([]any)
		var payload map[string]any
		_ = json.Unmarshal([]byte(messages[len(messages)-1].(map[string]any)["content"].(string)), &payload)
		id := payload["messages"].([]any)[0].(map[string]any)["id"]
		return responseCalls(map[string]any{"name": "learning.review_empty", "arguments": map[string]any{"content": "Synthetic correction", "kind": "fact", "sourceMessageIds": []any{id}}})
	})
	calls, extra, e := b.extract()
	if e != nil || !extra || len(calls) != 1 || calls[0].Name != "learning.propose" || count != 2 {
		t.Fatalf("review %v %v %d %v", calls, extra, count, e)
	}
	if e = b.repair(calls, extra); e != nil || count != 2 {
		t.Fatalf("shared extra budget %d %v", count, e)
	}
}
