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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestInstalledApplicationBackgroundLearningForegroundProgress(t *testing.T) {
	const learned = "HTTP defines request methods."
	const learningScene, sourceID = "fixture-learning-only", "fixture-learning-source"
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var resume sync.Once
	unblock := func() { resume.Do(func() { close(release) }) }
	var learning, embedding, decisions, plans, sends atomic.Int32
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
				len(request.Messages) != 2 || len(request.Tools) == 0 || request.Tools[0].Function.Name != "learning__propose" {
				t.Errorf("unexpected extraction request: %v", err)
				http.Error(w, "fixture extraction request invalid", 400)
				return
			}
			var payload struct {
				Messages []struct {
					ID    string
					Parts []struct{ Text string }
				}
			}
			if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil ||
				len(payload.Messages) != 1 || len(payload.Messages[0].Parts) != 1 || payload.Messages[0].Parts[0].Text != learned {
				t.Errorf("unexpected extraction source: %v", err)
				http.Error(w, "fixture extraction source invalid", 400)
				return
			}
			if learning.Add(1) != 1 {
				t.Error("learning batch repeated")
				http.Error(w, "fixture learning repeated", 400)
				return
			}
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			args := fmt.Sprintf(`{"content":%q,"kind":"fact","sourceMessageIds":[%q]}`, learned, payload.Messages[0].ID)
			fmt.Fprintf(w, `{"choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"proposal","type":"function","function":{"name":"learning__propose","arguments":%q}}]}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`, args)
		default:
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if err := serveMixedLoadModel(w, r, foregroundEntered, foregroundReady, &decisions, &plans, &sends); err != nil {
				t.Error(err)
				http.Error(w, "fixture foreground request invalid", 400)
			}
		}
	}))
	defer provider.Close()
	defer unblock()

	options := applicationOptions(t)
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		unblock()
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
		request, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
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
	call("PUT", plugin+"config", map[string]any{"timestamp": now})
	call("PUT", plugin+"enabled", map[string]bool{"enabled": true})
	initial := call("POST", plugin+"actions/inspect", map[string]any{})
	sceneID := field(field(initial, "scene"), "id").(string)
	scenePath := "/api/scenes/" + url.PathEscape(sceneID)
	foregroundFound := false
	for _, row := range call("GET", "/api/scenes", nil).([]any) {
		if field(row, "id") == sceneID {
			scene := row.(*jsjson.Object)
			_ = scene.Set("participationEnabled", true)
			_ = scene.Set("learningEnabled", false)
			call("PUT", scenePath, scene)
			foregroundFound = true
			break
		}
	}
	if !foregroundFound {
		t.Fatal("foreground adapter scene was not listed")
	}
	call("PUT", "/api/scenes/"+learningScene, map[string]any{
		"id": learningScene, "name": "Learning Fixture", "kind": "group", "adapterId": "synthetic", "accountId": "a", "platformSceneId": "l",
		"participationEnabled": false, "learningEnabled": true, "proactiveEnabled": false,
	})
	seed := memoryToolObject("id", sourceID, "sceneId", learningScene, "senderId", "learning-fixture-user", "origin", "live",
		"status", "available", "sentAt", now, "receivedAt", now, "parts", []any{memoryToolObject("type", "text", "text", learned)})
	if _, err := app.Engine.Runtime.Ingest(ctx, learningScene, seed, false); err != nil {
		t.Fatal(err)
	}
	if status := app.Learning.Coordinator.Status(); status.Accepted != 1 || status.Queued+status.Active != 1 || status.Completed != 0 {
		t.Fatalf("expected one admitted learning source before completion: %+v", status)
	}
	t.Log("installed ingress admitted one learning source before completion")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("installed learning timer never reached provider", ctx.Err())
	}
	checkHeld := func() {
		t.Helper()
		status := app.Learning.Coordinator.Status()
		if status.Active != 1 || status.Completed != 0 || status.Failed != 0 || embedding.Load() != 0 {
			t.Fatalf("learning unexpectedly settled or indexed before release: %+v", status)
		}
	}
	checkHeld()
	call("POST", plugin+"actions/new-source", map[string]any{})
	wait("foreground did not complete while learning held", func() bool {
		view, err := app.Engine.Runtime.Snapshot(sceneID)
		return err == nil && view.Active == 0 && view.Pending == 0 && view.Waiting == 0 &&
			slices.ContainsFunc(view.Messages, func(row *jsjson.Object) bool { return field(row, "senderId") == "bot" })
	})
	dispatch := call("POST", plugin+"actions/inspect", map[string]any{})
	if len(field(dispatch, "sent").([]any)) != 1 || field(dispatch, "source") == field(initial, "source") ||
		decisions.Load() != 1 || plans.Load() != 1 || sends.Load() != 1 {
		t.Fatal("expected one fresh real adapter receipt")
	}
	items := field(call("GET", scenePath+"/history?source=local", nil), "items").([]any)
	if !slices.ContainsFunc(items, func(row any) bool {
		return field(row, "senderId") == "bot" &&
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
	countLearned := func() int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories WHERE json_extract(value,'$.content')=?`, learned).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	checkHeld()
	if countLearned() != 0 {
		t.Fatal("queued learning was mistaken for durable memory")
	}
	t.Log("while learning provider was held: one adapter ACK, authenticated history and usage reads, committed settings write, no learned row")
	unblock()
	wait("learning did not complete after release", func() bool {
		return app.Learning.Coordinator.Status().Completed == 1
	})
	if countLearned() != 1 || learning.Load() != 1 || embedding.Load() != 1 {
		t.Fatalf("expected one durable learned row and one extraction/embedding: models=%d vectors=%d", learning.Load(), embedding.Load())
	}
	var stored string
	if err := db.QueryRowContext(ctx, `SELECT value FROM memories WHERE json_extract(value,'$.content')=?`, learned).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	row, err := jsjson.Parse(stored)
	if err != nil {
		t.Fatal(err)
	}
	sources, ok := field(row, "sources").([]any)
	if !ok || len(sources) != 1 || field(sources[0], "sceneId") != learningScene || field(sources[0], "messageId") != sourceID {
		t.Fatal("durable memory did not retain exact installed learning source")
	}
	if rows := usage(); rows["learning"] != 1 || rows["embedding"] != 1 {
		t.Fatalf("completed learning/embedding accounting missing: %v", rows)
	}
	t.Log("after release: completed learning, one durable assertion with exact source, one learning and one embedding usage receipt")
}
