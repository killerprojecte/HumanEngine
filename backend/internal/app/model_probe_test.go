package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestModelProbeActualSource(t *testing.T) {
	type exchange struct {
		Path string
		Body any
	}
	type scenario struct {
		Name, ID, Provider, Payload string
		Unauthorized                bool
		Status, Error               int
		Reply                       json.RawMessage
		Body                        map[string]any
		Wire                        []exchange
		Guest                       []any
		Headers                     map[string]string
	}
	var fixture struct {
		Models map[string]any
		Cases  []scenario
	}
	raw, err := os.ReadFile("testdata/model-probe-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) != 26 {
		t.Fatalf("source oracle %d: %v", len(fixture.Cases), err)
	}
	var mu sync.Mutex
	var current scenario
	actual := []exchange{}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data any
		parseErr := json.NewDecoder(r.Body).Decode(&data)
		mu.Lock()
		active := current
		actual = append(actual, exchange{Path: r.URL.Path, Body: data})
		mu.Unlock()
		if parseErr != nil || active.Reply == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		status := active.Error
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(active.Reply)
	}))
	defer peer.Close()
	for _, value := range fixture.Models["providers"].([]any) {
		value.(map[string]any)["baseUrl"] = peer.URL + "/v1"
	}
	options := applicationOptions(t)
	plugin, err := os.ReadFile("testdata/model-probe-plugin.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.PluginRoot, "probe.mjs"), plugin, 0600); err != nil {
		t.Fatal(err)
	}
	options.Entries = []string{"probe.mjs"}
	options.HTTP = peer.Client()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	app, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	appRequest(t, app, http.MethodPut, "/api/plugins/fixture/enabled", map[string]any{"enabled": true})
	appRequest(t, app, http.MethodPut, "/api/models", fixture.Models)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Name == "held-native" {
				db, err := sql.Open("sqlite3", options.Installation.Database)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.ExecContext(ctx, "CREATE TABLE recovery_model_hold(id INTEGER PRIMARY KEY,restore_id TEXT,status TEXT); INSERT INTO recovery_model_hold VALUES(1,'12345678-1234-4123-8123-123456789012','held')"); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			current, actual = c, []exchange{}
			mu.Unlock()
			id := c.ID
			if id == "" {
				id = c.Provider
			}
			if id == "" {
				id = "chat"
			}
			r := httptest.NewRequest(http.MethodPost, "/api/models/"+id+"/probe", strings.NewReader(c.Payload)).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			const suppliedID = "12345678-1234-4123-8123-123456789012"
			r.Header.Set("X-Request-ID", suppliedID)
			sourceFixtureAuth(r, !c.Unauthorized)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, r)
			if w.Code != c.Status {
				t.Fatalf("HTTP %d: %s; want %d", w.Code, w.Body, c.Status)
			}
			for k, want := range c.Headers {
				if got := w.Header().Get(k); got != want {
					t.Errorf("%s=%q, want %q", k, got, want)
				}
			}
			requestID := w.Header().Get("X-Request-ID")
			if len(requestID) != 36 || requestID == suppliedID {
				t.Fatalf("request id not fresh: %q", requestID)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["requestId"] != nil {
				if body["requestId"] != requestID {
					t.Fatal("request ID mismatch")
				}
				body["requestId"] = "<request-id>"
			}
			if c.Status == 200 {
				ms, ok := body["durationMs"].(float64)
				if !ok || ms < 0 {
					t.Fatalf("invalid duration: %v", body["durationMs"])
				}
				delete(body, "durationMs")
			}
			if !reflect.DeepEqual(body, c.Body) {
				t.Errorf("body %#v, want %#v", body, c.Body)
			}
			mu.Lock()
			got := actual
			mu.Unlock()
			if !reflect.DeepEqual(got, c.Wire) {
				t.Errorf("loopback wire %#v, want %#v", got, c.Wire)
			}
			if strings.HasPrefix(c.Name, "v1-") || c.Name == "held-custom" {
				reply := appRequest(t, app, http.MethodPost, "/api/plugins/fixture/actions/probe-calls", map[string]any{})
				wire, err := jsjson.Marshal(reply)
				if err != nil {
					t.Fatal(err)
				}
				var guest []any
				if err := json.Unmarshal(wire, &guest); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(guest, c.Guest) {
					t.Errorf("V1 wire %#v, want %#v", guest, c.Guest)
				}
			}
		})
	}
}
