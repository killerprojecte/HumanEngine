package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestSimulationsActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/simulations-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scene json.RawMessage
		Cases []struct {
			Method, Path string
			Authorized   bool
			Input        json.RawMessage
			Status       int
			CacheControl string
			Body         any
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	options := applicationOptions(t)
	ctx := context.Background()
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	appRequest(t, app, http.MethodPut, "/api/scenes/real", fixture.Scene)
	settings := appRequest(t, app, http.MethodGet, "/api/settings", nil)
	// Enable real source capture so persistence is observable if simulation
	// accidentally takes a real ingress path.
	if err := settings.(*botruntime.Object).Set("sourceEventRevisionsEnabled", true); err != nil {
		t.Fatal(err)
	}
	settingsBody, err := jsjson.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	appRequest(t, app, http.MethodPut, "/api/settings", json.RawMessage(settingsBody))
	idPattern := regexp.MustCompile(`^sim:[0-9a-f-]{36}$`)
	var simulation string
	for _, tc := range fixture.Cases {
		t.Run(tc.Method+tc.Path, func(t *testing.T) {
			path := strings.ReplaceAll(tc.Path, "{simulation}", url.PathEscape(simulation))
			input := bytes.ReplaceAll(tc.Input, []byte("{astral}"), []byte(strings.Repeat("😀", 6001)))
			input = bytes.ReplaceAll(input, []byte("{overflow}"), []byte(strings.Repeat("😀", 12001)))
			req := httptest.NewRequest(tc.Method, path, bytes.NewReader(input))
			sourceFixtureAuth(req, tc.Authorized)
			if len(input) != 0 {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			app.ServeHTTP(w, req)
			if w.Code != tc.Status || w.Header().Get("Cache-Control") != tc.CacheControl {
				t.Fatalf("status/cache-control: %d %q: %s", w.Code, w.Header().Get("Cache-Control"), w.Body)
			}
			var got any
			if w.Body.Len() != 0 {
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
			}
			if tc.Method == http.MethodPost && tc.Path == "/api/simulations" && w.Code == http.StatusOK {
				body := got.(map[string]any)
				simulation, _ = body["id"].(string)
				if !idPattern.MatchString(simulation) {
					t.Fatalf("invalid simulation ID: %q", simulation)
				}
				body["id"] = "{simulation}"
			}
			if body, ok := got.(map[string]any); ok && body["requestId"] != nil {
				if body["requestId"] != w.Header().Get("X-Request-ID") {
					t.Fatal("missing diagnostic correlation")
				}
				body["requestId"] = "{requestId}"
			}
			if rows, ok := got.([]any); ok {
				for _, value := range rows {
					row := value.(map[string]any)
					if id, _ := row["id"].(string); id == "" || row["sceneId"] != simulation {
						t.Fatal("message escaped simulation scope")
					}
					row["id"], row["sceneId"] = "{message}", "{simulation}"
					for _, key := range []string{"sentAt", "receivedAt"} {
						if _, err := time.Parse(time.RFC3339Nano, row[key].(string)); err != nil {
							t.Fatal(err)
						}
						row[key] = "{timestamp}"
					}
					for _, value := range row["parts"].([]any) {
						part := value.(map[string]any)
						if part["text"] == strings.Repeat("😀", 6001) {
							part["text"] = "{astral}"
						}
					}
				}
			}
			if !reflect.DeepEqual(got, tc.Body) {
				t.Fatalf("response = %#v; want %#v", got, tc.Body)
			}
		})
	}
	scenes := appRequest(t, app, http.MethodGet, "/api/scenes", nil).([]any)
	if len(scenes) != 1 || field(scenes[0], "id") != "real" {
		t.Fatal("simulation modified persisted scene inventory")
	}
	if _, ok := app.Engine.Runtime.RegisteredScene("real"); !ok {
		t.Fatal("simulation DELETE removed real scene")
	}
	// Exercise an actual installed provider, Planner/Replyer, and real adapter.
	// A simulated reply must stay in its transcript and never reach the adapter.
	appRequest(t, app, http.MethodPut, "/api/models", json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"runtime-fixture:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`))
	appRequest(t, app, http.MethodPut, "/api/plugins/runtime-fixture/config", map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
	appRequest(t, app, http.MethodPut, "/api/plugins/runtime-fixture/enabled", map[string]any{"enabled": true})
	live := appRequest(t, app, http.MethodPost, "/api/plugins/runtime-fixture/actions/inspect", map[string]any{})
	liveScene, _ := field(field(live, "scene"), "id").(string)
	if liveScene == "" {
		t.Fatal("real adapter was not installed")
	}
	previousSimulation := simulation
	simulation = field(appRequest(t, app, http.MethodPost, "/api/simulations", nil), "id").(string)
	// A saved real configuration may use an arbitrary ID. It must not replace
	// the active simulation's in-process identity.
	var colliding map[string]any
	if err := json.Unmarshal(fixture.Scene, &colliding); err != nil {
		t.Fatal(err)
	}
	colliding["id"] = simulation
	appRequest(t, app, http.MethodPut, "/api/scenes/"+url.PathEscape(simulation), colliding)
	path := "/api/simulations/" + url.PathEscape(simulation)
	rows := appRequest(t, app, http.MethodPost, path+"/messages", map[string]any{"text": "Synthetic configured turn"}).([]any)
	if len(rows) != 2 || field(rows[1], "senderId") != "bot" || field(rows[1], "origin") != "simulation" {
		t.Fatalf("missing isolated simulated reply: %+v", rows)
	}
	if got := appRequest(t, app, http.MethodGet, path, nil).([]any); len(got) != 2 {
		t.Fatal("configured transcript was not retained")
	}
	live = appRequest(t, app, http.MethodPost, "/api/plugins/runtime-fixture/actions/inspect", map[string]any{})
	if field(live, "modelCalls").(float64) == 0 || len(field(live, "sent").([]any)) != 0 {
		t.Fatal("simulation did not reach the model or was sent to the real adapter")
	}
	db, err := sql.Open("sqlite3", options.Installation.Database+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM source_event_heads WHERE scene_id=?", liveScene).Scan(&count); err != nil || count == 0 {
		t.Fatalf("no real source witness: count=%d err=%v", count, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM source_event_heads WHERE scene_id IN (?,?)", previousSimulation, simulation).Scan(&count); err != nil || count != 0 {
		t.Fatalf("simulation persisted source events: count=%d err=%v", count, err)
	}
}
