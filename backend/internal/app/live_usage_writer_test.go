package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
)

func TestApplicationLiveModelUsageActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/live-usage-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Node, ProviderID string
		Calls            []struct {
			Role, ModelID string
			Raw           json.RawMessage
		}
		Views []struct {
			Path string
			Body []map[string]any
		}
	}
	if err := json.Unmarshal(raw, &source); err != nil || source.Node != "v24.22.1" {
		t.Fatalf("invalid source usage fixture: %s %v", source.Node, err)
	}
	var mu sync.Mutex
	next := 0
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body struct{ Model string }
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&body) != nil ||
			next >= len(source.Calls) || body.Model != source.Calls[next].ModelID {
			t.Errorf("unexpected synthetic provider call %s %q at %d", r.URL.Path, body.Model, next)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":` +
			string(source.Calls[next].Raw) + "}"))
		next++
	}))
	defer local.Close()

	ctx := t.Context()
	app, db := memoryDeleteApplication(t, "synthetic-token")
	models := map[string]any{
		"providers": []any{map[string]any{"id": source.ProviderID, "name": "Synthetic", "protocol": "openai-chat", "baseUrl": local.URL + "/v1", "retryCount": 0}},
		"models":    []any{},
		"routes":    []any{},
	}
	for _, call := range []int{0, 2} {
		c := source.Calls[call]
		models["models"] = append(models["models"].([]any), map[string]any{
			"id": c.ModelID, "providerId": source.ProviderID, "displayName": c.ModelID, "apiModelName": c.ModelID,
			"capabilities": []string{"text"}, "inputUsdPerMillion": 2, "outputUsdPerMillion": 4, "cacheReadUsdPerMillion": 1,
		})
		models["routes"] = append(models["routes"].([]any), map[string]any{"role": c.Role, "orderedModelIds": []string{c.ModelID}, "timeoutMs": 30000})
	}
	appRequest(t, app, http.MethodPut, "/api/models", models)
	for _, call := range source.Calls {
		reply, err := app.models.Complete(ctx, providertransport.CompletionRequest{Role: call.Role, Messages: []*providertransport.Message{
			{Visible: map[string]any{"role": "user", "content": "Synthetic metering"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		reply.Release()
	}
	if err := app.models.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	received := next
	mu.Unlock()
	if received != 3 {
		t.Fatalf("provider saw %d calls", received)
	}
	for role, want := range map[string]int{"planner": 2, "replyer": 1} {
		var observed int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM model_calls WHERE role=? AND status='completed'", role).Scan(&observed); err != nil || observed != want {
			t.Fatalf("after physical drain model_calls %s=%d, want %d: %v", role, observed, want, err)
		}
	}
	for _, view := range source.Views {
		t.Run(strings.TrimPrefix(view.Path, "/api/usage"), func(t *testing.T) {
			body := appRequest(t, app, http.MethodGet, view.Path, nil)
			var rows []map[string]any
			encoded, err := jsjson.Marshal(body)
			if err != nil || json.Unmarshal(encoded, &rows) != nil {
				t.Fatalf("invalid usage: %s %v", encoded, err)
			}
			for _, row := range rows {
				if _, err := time.Parse("2006-01-02", row["day"].(string)); err != nil {
					t.Fatal(err)
				}
				delete(row, "day")
				delete(row, "durationMs")
			}
			if !reflect.DeepEqual(rows, view.Body) {
				t.Errorf("live usage missing or not equivalent after three observed real attempts: got %s; want %+v", encoded, view.Body)
			}
		})
	}
}
