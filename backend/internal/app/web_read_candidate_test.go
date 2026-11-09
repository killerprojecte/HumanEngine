//go:build web_read_candidate

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

// This test executes the installed guest through the real planner/tool bridge.
// Its URL is a private literal: the guest rejects it before DNS or sockets, so
// this test cannot contact a public site. TCP/TLS/DNS/cache are separate suites.
func TestInstalledWebReadCandidateLifecycle(t *testing.T) {
	options := applicationOptions(t)
	options.Builtins = []string{"web-read"}
	fixture, err := os.ReadFile("testdata/web-read-model.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(options.PluginRoot, "application.mjs"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	// Like the installed Snow fixture, the total test budget covers repeated
	// real cold compilation and physical unload. Each admin operation has its
	// own deadline below. Production request and drain timeouts are unchanged.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	opened := time.Now()
	t.Log("phase start application Open with real guest admission")
	a, err := Open(ctx, options)
	t.Logf("phase finish application Open after %s", time.Since(opened))
	if err != nil {
		if a != nil {
			_ = a.Shutdown(context.Background())
		}
		t.Fatal(err)
	}
	defer func() {
		if err := a.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	// These are genuine HTTP handlers with a bounded test request context, not
	// changes to application request policy or shortcuts around guest validation.
	webAdmin := func(name, path string, body any) {
		t.Helper()
		phase, stop := context.WithTimeout(ctx, 3*time.Minute)
		defer stop()
		if err := phase.Err(); err != nil {
			t.Fatal("admin phase started after fixture deadline", name, err)
		}
		started := time.Now()
		t.Logf("phase start %s %s", name, path)
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(raw)).WithContext(phase)
		request.Header.Set("Authorization", "Bearer synthetic-token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		a.ServeHTTP(response, request)
		t.Logf("phase finish %s after %s", name, time.Since(started))
		if phase.Err() != nil || response.Code != http.StatusOK {
			t.Fatalf("%s: context=%v status=%d %s", name, phase.Err(), response.Code, response.Body.String())
		}
	}
	assertRetired := func(name string, call func(context.Context, context.Context, []byte) ([]byte, error)) {
		t.Helper()
		phase, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := phase.Err(); err != nil {
			t.Fatal("stale-generation assertion began with cancelled context", name, err)
		}
		_, err := call(phase, phase, []byte(`{"operation":"registrations"}`))
		if phase.Err() != nil || !errors.Is(err, plugins.ErrNotLoaded) {
			t.Fatal("stale generation must reject as not loaded, never as deadline/cancellation", name, err, phase.Err())
		}
	}
	if a.Bridge.Host.PID() != 0 {
		t.Fatal("builtin registration started Node")
	}
	if _, err = a.WASM.BindCall("web-read-policy"); err == nil {
		t.Fatal("registration retained idle policy guest")
	}
	webAdmin("initial configuration", "/api/plugins/web-read/config", map[string]any{"maxChars": 256, "maxResponseBytes": 0})
	webAdmin("initial enable", "/api/plugins/web-read/enabled", map[string]any{"enabled": true})
	if a.Bridge.Host.PID() != 0 {
		t.Fatal("builtin enable started Node")
	}
	old, err := a.WASM.BindCallWithAdmission("web-read")
	if err != nil {
		t.Fatal(err)
	}
	settings := appRequest(t, a, http.MethodGet, "/api/settings", nil).(*jsjson.Object)
	_ = settings.Set("sourceEventRevisionsEnabled", true)
	_ = settings.Set("promptContextWindowEnabled", false)
	_ = settings.Set("debounceMs", float64(0))
	_ = settings.Set("groupTurnCooldownMs", float64(0))
	raw, _ := jsjson.Marshal(settings)
	appRequest(t, a, http.MethodPut, "/api/settings", json.RawMessage(raw))
	models := json.RawMessage(`{"providers":[{"id":"p","name":"Synthetic","protocol":"web-model:deterministic","baseUrl":"https://synthetic.invalid","retryCount":0,"timeoutMs":10000}],"models":[{"id":"m","displayName":"Synthetic","providerId":"p","apiModelName":"synthetic","capabilities":["text","tools"]}],"routes":[{"role":"planner","orderedModelIds":["m"],"timeoutMs":10000},{"role":"replyer","orderedModelIds":["m"],"timeoutMs":10000}]}`)
	appRequest(t, a, http.MethodPut, "/api/models", models)
	appRequest(t, a, http.MethodPut, "/api/plugins/web-model/config", map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "receivedMarker": filepath.Join(options.PluginRoot, "received.marker")})
	appRequest(t, a, http.MethodPut, "/api/plugins/web-model/enabled", map[string]any{"enabled": true})
	inspect := func() any {
		return appRequest(t, a, http.MethodPost, "/api/plugins/web-model/actions/inspect", map[string]any{})
	}
	sceneID := field(field(inspect(), "scene"), "id").(string)
	var scene *jsjson.Object
	for _, v := range appRequest(t, a, http.MethodGet, "/api/scenes", nil).([]any) {
		if field(v, "id") == sceneID {
			scene = v.(*jsjson.Object)
		}
	}
	if scene == nil {
		t.Fatal("scene missing")
	}
	_ = scene.Set("participationEnabled", true)
	raw, _ = jsjson.Marshal(scene)
	appRequest(t, a, http.MethodPut, "/api/scenes/"+url.PathEscape(sceneID), json.RawMessage(raw))
	waitSent := func(want int) any {
		t.Helper()
		turn, stop := context.WithTimeout(ctx, 150*time.Second)
		defer stop()
		started := time.Now()
		t.Logf("phase start planner receipt %d", want)
		for {
			if err := turn.Err(); err != nil {
				t.Fatal("web-read installed turn did not send", want, err)
			}
			value := inspect()
			if len(field(value, "sent").([]any)) >= want {
				t.Logf("phase finish planner receipt %d after %s", want, time.Since(started))
				return value
			}
			select {
			case <-turn.Done():
				t.Fatal("web-read installed turn did not send", turn.Err())
				return nil
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	appRequest(t, a, http.MethodPost, "/api/plugins/web-model/actions/new-source", map[string]any{})
	first := waitSent(1)
	if len(field(first, "webReceipts").([]any)) != 1 {
		t.Fatal("real web-read result absent", first)
	}
	webAdmin("hot configuration", "/api/plugins/web-read/config", map[string]any{"maxChars": 512, "maxConcurrency": 1})
	assertRetired("hot configuration", old)
	active, err := a.WASM.BindCallWithAdmission("web-read")
	if err != nil {
		t.Fatal(err)
	}
	webAdmin("disable", "/api/plugins/web-read/enabled", map[string]any{"enabled": false})
	assertRetired("disable", active)
	if _, err = a.WASM.BindCall("web-read"); err == nil {
		t.Fatal("disabled service was not unloaded")
	}
	if _, err = a.WASM.BindCall("web-read-policy"); err == nil {
		t.Fatal("disable retained policy guest")
	}
	webAdmin("disabled configuration", "/api/plugins/web-read/config", map[string]any{"maxChars": 256})
	appRequest(t, a, http.MethodGet, "/api/plugins", nil)
	if _, err = a.WASM.BindCall("web-read-policy"); err == nil {
		t.Fatal("disabled config/list retained policy guest")
	}
	appRequest(t, a, http.MethodPost, "/api/plugins/web-model/actions/expect-web", map[string]any{"enabled": false})
	appRequest(t, a, http.MethodPost, "/api/plugins/web-model/actions/new-source", map[string]any{})
	second := waitSent(2)
	if len(field(second, "webReceipts").([]any)) != 1 {
		t.Fatal("disabled tool was executed", second)
	}
	webAdmin("re-enable", "/api/plugins/web-read/enabled", map[string]any{"enabled": true})
	appRequest(t, a, http.MethodPost, "/api/plugins/web-model/actions/expect-web", map[string]any{"enabled": true})
	appRequest(t, a, http.MethodPost, "/api/plugins/web-model/actions/new-source", map[string]any{})
	third := waitSent(3)
	if len(field(third, "webReceipts").([]any)) != 2 {
		t.Fatal("re-enabled tool did not execute", third)
	}
	assertRetired("re-enable", active)
	appRequest(t, a, http.MethodPut, "/api/plugins/web-model/enabled", map[string]any{"enabled": false})
	if a.Bridge.Host.PID() != 0 {
		t.Fatal("web-read retained a V1 process")
	}
}
