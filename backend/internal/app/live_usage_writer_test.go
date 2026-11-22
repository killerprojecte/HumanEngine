package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	testApplicationLiveModelUsageActualSource(t, false, "testdata/live-usage-source.json")
}

func TestApplicationLiveSSEModelUsageActualSource(t *testing.T) {
	testApplicationLiveModelUsageActualSource(t, true, "testdata/live-usage-sse-source.json")
}

func testApplicationLiveModelUsageActualSource(t *testing.T, stream bool, fixture string) {
	t.Helper()
	raw, err := os.ReadFile(fixture)
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
		var body struct {
			Model         string
			Stream        bool
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&body) != nil ||
			next >= len(source.Calls) || body.Model != source.Calls[next].ModelID ||
			stream && (!body.Stream || !body.StreamOptions.IncludeUsage) {
			t.Errorf("unexpected synthetic provider call %s %q at %d", r.URL.Path, body.Model, next)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		usage := source.Calls[next].Raw
		next++
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			// Some successful streams never report usage. A supported provider
			// otherwise sends its usage after the finish chunk with no choices.
			if string(usage) != "null" {
				compact, err := json.Marshal(usage)
				if err != nil {
					t.Errorf("invalid usage fixture: %v", err)
					return
				}
				fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":%s}\n\n", compact)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":` +
			string(usage) + "}"))
	}))
	defer local.Close()

	ctx := t.Context()
	app, db := memoryDeleteApplication(t, "synthetic-token")
	models := map[string]any{
		"providers": []any{map[string]any{"id": source.ProviderID, "name": "Synthetic", "protocol": "openai-chat", "baseUrl": local.URL + "/v1", "retryCount": 0}},
		"models":    []any{},
		"routes":    []any{},
	}
	wantCalls := map[string]int{}
	for _, c := range source.Calls {
		wantCalls[c.Role]++
		if wantCalls[c.Role] != 1 {
			continue
		}
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
		if reply.Text != "ok" {
			t.Errorf("fixture completion %q", reply.Text)
		}
		reply.Release()
	}
	if err := app.models.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	received := next
	mu.Unlock()
	if received != len(source.Calls) {
		t.Fatalf("provider saw %d calls", received)
	}
	for role, want := range wantCalls {
		var observed int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM model_calls WHERE role=? AND status='completed'", role).Scan(&observed); err != nil || observed != want {
			t.Fatalf("after physical drain model_calls %s=%d, want %d: %v", role, observed, want, err)
		}
	}
	if stream {
		rows, err := db.QueryContext(ctx, `SELECT role, status, transport_mode, first_token_kind,
			input_tokens, output_tokens, cache_read_tokens FROM model_calls ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for _, c := range source.Calls {
			var reported *struct {
				Input  *int64 `json:"prompt_tokens"`
				Output *int64 `json:"completion_tokens"`
				Read   *int64 `json:"prompt_cache_hit_tokens"`
			}
			if err := json.Unmarshal(c.Raw, &reported); err != nil || !rows.Next() {
				t.Fatalf("missing durable attempt for %s %s: %v %v", c.Role, c.Raw, err, rows.Err())
			}
			var role, status, mode, kind string
			var tokens, want [3]sql.NullInt64
			if err := rows.Scan(&role, &status, &mode, &kind, &tokens[0], &tokens[1], &tokens[2]); err != nil {
				t.Fatal(err)
			}
			if reported != nil {
				for i, counter := range []*int64{reported.Input, reported.Output, reported.Read} {
					if counter != nil {
						want[i] = sql.NullInt64{Int64: *counter, Valid: true}
					}
				}
			}
			if role != c.Role || status != "completed" || mode != "stream" || kind != "text" || tokens != want {
				t.Fatalf("durable attempt %s/%s/%s/%s has counters %v; want %s completed/stream/text with %v", role, status, mode, kind, tokens, c.Role, want)
			}
		}
		if rows.Next() || rows.Err() != nil {
			t.Fatalf("unexpected durable attempt after clean EOF: %v", rows.Err())
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
				t.Errorf("live usage missing or not equivalent after drained observed attempts: got %s; want %+v", encoded, view.Body)
			}
		})
	}
}
