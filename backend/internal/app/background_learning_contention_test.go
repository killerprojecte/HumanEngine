package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestInstalledApplicationBackgroundLearningForegroundProgress(t *testing.T) {
	const sourceA = "fixture-learning-source"
	const followup = "Please remember that I always prefer concise replies."
	texts := [2]string{"HTTP defines request methods.", "The user prefers concise replies."}
	sources := [2]string{sourceA}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	entered := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	var resume [2]sync.Once
	unblock := func(i int) { resume[i].Do(func() { close(release[i]) }) }
	cleanup := func() { unblock(0); unblock(1) }
	var learning, reconciliation, embedding, decisions, plans, sends atomic.Int32
	foregroundEntered, foregroundReady := make(chan struct{}), make(chan struct{})
	close(foregroundReady)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture body unreadable", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/embeddings":
			embedding.Add(1)
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
		case bytes.Contains(raw, []byte(`"model":"held-learning"`)):
			var request struct {
				Messages []struct{ Role, Content string }
				Tools    []struct{ Function struct{ Name string } }
			}
			if err := json.Unmarshal(raw, &request); err != nil || r.URL.Path != "/v1/chat/completions" ||
				len(request.Messages) != 2 || len(request.Tools) == 0 {
				t.Errorf("unexpected extraction request: %v", err)
				http.Error(w, "fixture extraction request invalid", 400)
				return
			}
			switch request.Tools[0].Function.Name {
			case "learning__reconcile":
				payload := request.Messages[1].Content
				if learning.Load() != 2 || reconciliation.Add(1) != 1 ||
					!strings.Contains(payload, texts[0]) || !strings.Contains(payload, texts[1]) {
					t.Error("unexpected reconciliation of the independent follow-up fact")
					http.Error(w, "fixture reconciliation invalid", 400)
					return
				}
				// Keep both facts, with neither a merge nor a supersession.
				fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":""}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
				return
			case "learning__propose":
			default:
				t.Error("unexpected learning tool")
				http.Error(w, "fixture learning tool invalid", 400)
				return
			}
			var payload struct {
				Messages []struct {
					ID    string
					Parts []struct{ Text string }
				}
			}
			if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil ||
				len(payload.Messages) != 1 || len(payload.Messages[0].Parts) != 1 {
				t.Errorf("unexpected extraction source: %v", err)
				http.Error(w, "fixture extraction source invalid", 400)
				return
			}
			i := int(learning.Add(1)) - 1
			if i >= len(entered) {
				t.Error("unexpected repeated learning batch")
				http.Error(w, "fixture learning repeated", 400)
				return
			}
			source := payload.Messages[0]
			// The model may see an alias for the V1 source. The durable assertion below checks its original ID.
			if source.ID == "" || source.Parts[0].Text != [2]string{texts[0], followup}[i] {
				t.Errorf("batch %d did not receive its expected source", i+1)
				http.Error(w, "fixture learned wrong source", 400)
				return
			}
			close(entered[i])
			select {
			case <-release[i]:
			case <-r.Context().Done():
				return
			}
			args := fmt.Sprintf(`{"content":%q,"kind":"fact","sourceMessageIds":[%q]}`, texts[i], payload.Messages[0].ID)
			fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"proposal","type":"function","function":{"name":"learning__propose","arguments":%q}}]}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`, args)
		default:
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if err := serveMixedLoadModelTo(w, r, foregroundEntered, foregroundReady, &decisions, &plans, &sends, followup); err != nil {
				t.Error(err)
				http.Error(w, "fixture foreground request invalid", 400)
			}
		}
	}))
	defer provider.Close()
	defer cleanup()

	options := applicationOptions(t)
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup()
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	server := httptest.NewServer(app)
	defer server.Close()
	call := func(method, path string, body any) any {
		t.Helper()
		var raw []byte
		var err error
		if obj, ok := body.(*jsjson.Object); ok {
			raw, err = jsjson.Marshal(obj)
		} else {
			raw, err = json.Marshal(body)
		}
		if err != nil {
			t.Fatal(err)
		}
		requestCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		request, err := http.NewRequestWithContext(requestCtx, method, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		sourceFixtureAuth(request, true)
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		result, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("%s %s: %d %s (%v)", method, path, response.StatusCode, result, err)
		}
		value, err := jsjson.ParseBytes(result)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	wait := func(phase string, complete func() bool) {
		t.Helper()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for !complete() {
			select {
			case <-ctx.Done():
				t.Fatal(phase, ctx.Err())
			case <-tick.C:
			}
		}
	}
	settings := call("GET", "/api/settings", nil).(*jsjson.Object)
	for k, v := range map[string]any{
		"sourceEventRevisionsEnabled": true, "promptContextWindowEnabled": false,
		"debounceMs": float64(0), "groupTurnCooldownMs": float64(0), "modelConcurrency": float64(2),
		"learningBatchMs": float64(750), "learningMaxWaitMs": float64(750), "learningMinIntervalMs": float64(0),
		"learningRetryAttempts": float64(1),
	} {
		if err := settings.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	call("PUT", "/api/settings", settings)
	models := fmt.Sprintf(`{"providers":[{"id":"loopback","name":"Fixture","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":30000}],"models":[{"id":"m","providerId":"loopback","displayName":"Foreground","apiModelName":"mixed-load","capabilities":["text","tools"]},{"id":"l","providerId":"loopback","displayName":"Learning","apiModelName":"held-learning","capabilities":["text","tools"]},{"id":"e","providerId":"loopback","displayName":"Embedding","apiModelName":"fixture-embedding","capabilities":["embedding"],"embeddingDimensions":2}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":30000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":30000},{"role":"learning","orderedModelIds":["l"],"timeoutMs":30000},{"role":"embedding","orderedModelIds":["e"],"timeoutMs":30000}]}`, provider.URL+"/v1")
	call("PUT", "/api/models", json.RawMessage(models))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	plugin := "/api/plugins/runtime-fixture/"
	call("PUT", plugin+"config", map[string]any{"timestamp": now, "followupText": followup})
	call("PUT", plugin+"enabled", map[string]bool{"enabled": true})
	initial := call("POST", plugin+"actions/inspect", map[string]any{})
	sceneID := field(field(initial, "scene"), "id").(string)
	scenePath := "/api/scenes/" + url.PathEscape(sceneID)
	initialSource := field(initial, "source").(string)
	wait("adapter startup source did not leave learning disabled", func() bool {
		return slices.ContainsFunc(call("GET", "/api/logs", nil).([]any), func(row any) bool {
			return field(row, "sceneId") == sceneID && field(row, "code") == "learning.admission" &&
				field(row, "status") == "skipped" && field(row, "reason") == "learning_disabled"
		})
	})
	var group *jsjson.Object
	for _, row := range call("GET", "/api/scenes", nil).([]any) {
		if field(row, "id") == sceneID {
			group = row.(*jsjson.Object)
			break
		}
	}
	if group == nil || field(group, "participationEnabled") != false {
		t.Fatal("foreground adapter scene was not listed with participation disabled")
	}
	_ = group.Set("learningEnabled", true)
	call("PUT", scenePath, group)
	seed := memoryToolObject("id", sourceA, "sceneId", sceneID, "senderId", "learning-fixture-user", "origin", "live",
		"status", "available", "sentAt", now, "receivedAt", now, "parts", []any{memoryToolObject("type", "text", "text", texts[0])})
	if _, err := app.Engine.Runtime.Ingest(ctx, sceneID, seed, false); err != nil {
		t.Fatal(err)
	}
	if status := app.Learning.Coordinator.Status(); status.Accepted != 1 || status.Queued+status.Active != 1 || status.Completed != 0 {
		t.Fatalf("expected one admitted learning source before completion: %+v", status)
	}
	t.Log("installed ingress admitted one learning source before completion")
	select {
	case <-entered[0]:
	case <-ctx.Done():
		t.Fatal("installed learning timer never reached provider", ctx.Err())
	}
	checkHeld := func(accepted, queued int) {
		t.Helper()
		status := app.Learning.Coordinator.Status()
		if status.Accepted != accepted || status.Queued != queued || status.Active != 1 || status.Completed != 0 || status.Failed != 0 || embedding.Load() != 0 || learning.Load() != 1 || reconciliation.Load() != 0 {
			t.Fatalf("learning unexpectedly settled or indexed before release: %+v", status)
		}
	}
	// Enable foreground only after A's ingress, so the startup input cannot
	// race the policy write and trigger an unrelated reply.
	_ = group.Set("participationEnabled", true)
	call("PUT", scenePath, group)
	checkHeld(1, 0)
	call("POST", plugin+"actions/new-source", map[string]any{})
	wait("foreground did not complete while learning held", func() bool {
		view, err := app.Engine.Runtime.Snapshot(sceneID)
		return err == nil && view.Active == 0 && view.Pending == 0 && view.Waiting == 0 &&
			slices.ContainsFunc(view.Messages, func(row *jsjson.Object) bool { return field(row, "senderId") == "bot" })
	})
	dispatch := call("POST", plugin+"actions/inspect", map[string]any{})
	sources[1] = field(dispatch, "source").(string)
	if len(field(dispatch, "sent").([]any)) != 1 || sources[1] == initialSource ||
		decisions.Load() != 1 || plans.Load() != 1 || sends.Load() != 1 {
		t.Fatal("expected one fresh real adapter receipt")
	}
	checkHeld(2, 1)
	items := field(call("GET", scenePath+"/history?source=local", nil), "items").([]any)
	if !slices.ContainsFunc(items, func(row any) bool {
		return field(row, "senderId") == "bot" && field(row, "replyToId") == sources[1] &&
			field(field(row, "parts").([]any)[0], "text") == "Synthetic mixed-load reply."
	}) {
		t.Fatal("acknowledged foreground send was not visible through authenticated history")
	}
	usage := func() map[string]int {
		t.Helper()
		out := map[string]int{}
		for _, row := range call("GET", "/api/usage", nil).([]any) {
			out[field(row, "role").(string)] += int(field(row, "calls").(float64))
		}
		return out
	}
	if rows := usage(); rows["planner"] != 2 || rows["replyer"] != 1 || rows["learning"] != 0 {
		t.Fatalf("foreground usage did not drain independently of learning: %v", rows)
	}
	_ = settings.Set("debugModelIO", true)
	call("PUT", "/api/settings", settings)
	if field(call("GET", "/api/settings", nil), "debugModelIO") != true {
		t.Fatal("authenticated settings did not commit while learning held")
	}
	db, err := sql.Open("sqlite3", options.Installation.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	countLearned := func(i int) int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE json_extract(value,'$.content')=?`, texts[i]).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	checkHeld(2, 1)
	if !slices.ContainsFunc(call("GET", "/api/scenes", nil).([]any), func(row any) bool {
		return field(row, "id") == sceneID && field(row, "learningEnabled") == true
	}) {
		t.Fatal("group learning was not enabled during contention")
	}
	if countLearned(0) != 0 || countLearned(1) != 0 {
		t.Fatal("queued learning was mistaken for durable memory")
	}
	t.Log("one group: second source queued for learning while its foreground ACK and authenticated management calls completed")
	unblock(0)
	select {
	case <-entered[1]:
	case <-ctx.Done():
		t.Fatal("queued second source did not enter its own batch", ctx.Err())
	}
	if status := app.Learning.Coordinator.Status(); status.Completed != 1 || status.Active != 1 || status.Queued != 0 || countLearned(0) != 1 || countLearned(1) != 0 {
		t.Fatalf("second batch did not start independently after first committed: %+v", status)
	}
	unblock(1)
	wait("both learning batches did not complete with independent trace linkage", func() bool {
		if app.Learning.Coordinator.Status().Completed != 2 {
			return false
		}
		batches, admissions := make(map[string]bool), make(map[string]bool)
		for _, row := range call("GET", "/api/logs", nil).([]any) {
			if field(row, "sceneId") != sceneID || field(row, "status") != "completed" {
				continue
			}
			batch, _ := field(row, "batchId").(string)
			parent, _ := field(row, "parentTraceId").(string)
			if field(row, "code") == "learning.batch" && batch != "" && parent != "" && field(row, "inputCount") == float64(1) {
				batches[batch], admissions[parent] = true, true
			}
		}
		return len(batches) == 2 && len(admissions) == 2
	})
	status := app.Learning.Coordinator.Status()
	if status.Accepted != 2 || status.Active+status.Queued+status.Failed+status.Retried != 0 || learning.Load() != 2 || reconciliation.Load() != 1 || embedding.Load() != 3 {
		t.Fatalf("unexpected terminal learning counters: %+v, %d extractions, %d reconciliations, %d embeddings", status, learning.Load(), reconciliation.Load(), embedding.Load())
	}
	for i, content := range texts {
		if countLearned(i) != 1 {
			t.Fatalf("batch %d did not store exactly one assertion", i+1)
		}
		var stored string
		if err := db.QueryRowContext(ctx, `SELECT value FROM memories WHERE json_extract(value,'$.content')=?`, content).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		row, err := jsjson.Parse(stored)
		if err != nil {
			t.Fatal(err)
		}
		provenance, ok := field(row, "sources").([]any)
		if !ok || len(provenance) != 1 || field(provenance[0], "sceneId") != sceneID || field(provenance[0], "messageId") != sources[i] {
			t.Fatalf("durable batch %d did not retain its exact scene and source", i+1)
		}
	}
	if rows := usage(); rows["learning"] != 3 || rows["embedding"] != 3 {
		t.Fatalf("completed learning/embedding accounting missing: %v", rows)
	}
	t.Log("both same-scene batches completed independently with distinct source provenance and accounting")
}
