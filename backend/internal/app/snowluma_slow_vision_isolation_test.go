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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/core"
)

func assertSlowVisionSceneIsolation(t *testing.T, parent context.Context, app *Application, main *sql.DB, emit func(any), png string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	const independentText = "Independent slow vision text fixture"
	vision, release, unblocked := make(chan []byte, 1), make(chan struct{}), make(chan struct{})
	close(unblocked)
	finish := sync.OnceFunc(func() { close(release) })
	var decisions, plans, sends atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "fixture body", 400)
			return
		}
		if bytes.Contains(raw, []byte(`"model":"held-vision"`)) {
			select {
			case vision <- raw:
			default:
				http.Error(w, "unexpected additional vision call", 400)
				return
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Synthetic vision finished."}}]}`)
			return
		}
		if bytes.Contains(raw, []byte(`"name":"participation__decide"`)) && !bytes.Contains(raw, []byte(independentText)) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"silent-a","type":"function","function":{"name":"participation__decide","arguments":"{\"action\":\"silent\",\"reasonCode\":\"fixture\"}"}}]}}]}`)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err := serveMixedLoadModelTo(w, r, make(chan struct{}), unblocked, &decisions, &plans, &sends, independentText); err != nil {
			t.Error(err)
			http.Error(w, "unexpected synthetic stage", 400)
		}
	}))
	defer provider.Close()
	defer finish()
	appRequest(t, app, "PUT", "/api/models", json.RawMessage(fmt.Sprintf(`{"providers":[{"id":"slow-vision","name":"Synthetic","protocol":"openai-chat","baseUrl":%q,"retryCount":0,"timeoutMs":15000}],"models":[{"id":"vlm","providerId":"slow-vision","displayName":"Vision","apiModelName":"held-vision","capabilities":["text","vision"]},{"id":"chat","providerId":"slow-vision","displayName":"Chat","apiModelName":"mixed-load","capabilities":["text","tools"]}],"routes":[{"role":"vision","orderedModelIds":["vlm"],"timeoutMs":15000},{"role":"planner","orderedModelIds":["chat"],"timeoutMs":15000},{"role":"replyer","orderedModelIds":["chat"],"timeoutMs":15000}]}`, provider.URL+"/v1")))
	settings := appRequest(t, app, "GET", "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("debounceMs", float64(8000))
	settingsWire, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, "PUT", "/api/settings", json.RawMessage(settingsWire))
	ingress := func(group, id int, part map[string]any) {
		emit(map[string]any{"self_id": 42, "post_type": "message", "message_type": "group", "group_id": group, "user_id": 7, "message_id": id, "time": time.Now().Unix(), "message": []any{part}})
	}
	await := func(scene, message string) botruntime.Snapshot {
		t.Helper()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			if s, err := app.Engine.Runtime.Snapshot(scene); err == nil {
				for _, row := range s.Messages {
					if field(row, "id") == message {
						return s
					}
				}
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				t.Fatalf("retained message absent: %s", message)
			}
		}
	}
	begin := func(group, id string) (*botruntime.TurnHandle, string, string) {
		t.Helper()
		scene := snow.SceneID("snowluma", "42", "group", group)
		snapshot := await(scene, snow.MessageID(scene, id))
		if len(snapshot.Topics) != 1 {
			t.Fatalf("expected one fixture topic: %+v", snapshot.Topics)
		}
		for _, candidate := range appRequest(t, app, "GET", "/api/scenes", nil).([]any) {
			if field(candidate, "id") == scene {
				config := candidate.(*jsjson.Object)
				_ = config.Set("participationEnabled", true)
				wire, err := jsjson.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				appRequest(t, app, "PUT", "/api/scenes/"+url.PathEscape(scene), json.RawMessage(wire))
				turn, err := app.Engine.Runtime.Run(scene, snapshot.Topics[0].ID, false)
				if err != nil {
					t.Fatal(err)
				}
				return turn, scene, snapshot.Topics[0].ID
			}
		}
		t.Fatal("missing live fixture scene")
		return nil, "", ""
	}
	ingress(143, 1, map[string]any{"type": "image", "data": map[string]any{"base64": png}})
	a, sceneA, topicA := begin("143", "1")
	select {
	case raw := <-vision:
		if !bytes.Contains(raw, []byte("data:image/png;base64,")) {
			t.Fatal("held VLM request did not contain real image data")
		}
	case <-ctx.Done():
		t.Fatal("no held native image request")
	}
	// The same scene may accept a new source while its topic turn is busy.
	ingress(143, 2, map[string]any{"type": "text", "data": map[string]any{"text": "Second same-scene source"}})
	second := snow.MessageID(sceneA, "2")
	snapshot := await(sceneA, second)
	for _, row := range snapshot.Messages {
		if field(row, "id") == second && field(row, "sourceEventRef") == nil {
			t.Fatal("second source did not retain its source-event reference")
		}
	}
	var durableHead string
	if err := main.QueryRowContext(ctx, "SELECT event_id FROM source_event_heads WHERE scene_id=? AND message_id=?", sceneA, second).Scan(&durableHead); err != nil || durableHead == "" {
		t.Fatalf("same-scene second ingress not committed while vision was held: %v", err)
	}
	if _, err := app.Engine.Runtime.Run(sceneA, topicA, false); err == nil || err.Error() != "topic_busy" {
		t.Fatalf("held topic permitted an overtaking turn: %v", err)
	}

	ingress(144, 1, map[string]any{"type": "text", "data": map[string]any{"text": independentText}})
	b, sceneB, _ := begin("144", "1")
	select {
	case <-b.Done:
		if result := b.Wait(); result.Status != "completed" {
			t.Fatalf("independent text turn failed before VLM release: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("independent text turn blocked behind foreign VLM")
	}
	await(sceneB, snow.MessageID(sceneB, "1499"))
	select {
	case <-a.Done:
		t.Fatal("held A unexpectedly ended before release")
	default:
	}
	if decisions.Load() != 1 || plans.Load() != 1 || sends.Load() != 1 {
		t.Fatalf("wrong independent wire stages: %d/%d/%d", decisions.Load(), plans.Load(), sends.Load())
	}
	finish()
	select {
	case <-a.Done:
		if result := a.Wait(); result.TurnID == "" {
			t.Fatal("held turn returned no terminal result")
		}
	case <-ctx.Done():
		t.Fatal("held vision did not drain")
	}
}
