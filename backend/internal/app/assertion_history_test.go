package app

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

//go:embed testdata/assertion-history-source.json
var assertionHistoryFixture []byte

func TestAssertionHistoryActualSource(t *testing.T) {
	var f struct {
		Node, Path                         string
		Scene, Record, Patch, SourcedInput json.RawMessage
		Source                             struct{ SceneID, MessageID string }
		Cases                              []struct {
			Name, URL, Method, Action  string
			Authorized, PrivateHeaders bool
			Status, Before, After      int
			Body                       map[string]any
		}
	}
	if err := json.Unmarshal(assertionHistoryFixture, &f); err != nil || f.Node != "v24.22.1" {
		t.Fatalf("invalid source fixture: %s %v", f.Node, err)
	}
	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	appRequest(t, app, http.MethodPut, "/api/scenes/scene", f.Scene)
	r := memoryPatchObject(t, f.Record)
	id := field(r, "id").(string)
	if _, err := db.ExecContext(ctx, "INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,'scene:scene',NULL,?,?,?)", id, field(r, "content"), id, string(f.Record)); err != nil {
		t.Fatal(err)
	}
	memory := app.Semantic.Memory
	sourced, err := memory.Create(ctx, memoryPatchObject(t, f.SourcedInput))
	if err != nil {
		t.Fatal(err)
	}
	sourcedID := field(sourced, "id").(string)
	uuid := regexp.MustCompile(`^[0-9a-f-]{36}$`)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Action {
			case "enable":
				settings := appRequest(t, app, http.MethodGet, "/api/settings", nil).(*jsjson.Object)
				_ = settings.Set("memoryHistoricalReadEnabled", true)
				encoded, err := jsjson.Marshal(settings)
				if err != nil {
					t.Fatal(err)
				}
				appRequest(t, app, http.MethodPut, "/api/settings", json.RawMessage(encoded))
			case "update":
				if _, err := memory.Update(ctx, id, memoryPatchObject(t, f.Patch)); err != nil {
					t.Fatal(err)
				}
			case "recall":
				if _, err := memory.Recall(ctx, f.Source.SceneID, f.Source.MessageID); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if ok, err := memory.Delete(ctx, id); err != nil || !ok {
					t.Fatalf("delete: %t %v", ok, err)
				}
			}
			if got := assertionVersion(t, ctx, db, id); got != c.Before {
				t.Fatalf("before version %d want %d", got, c.Before)
			}
			req := httptest.NewRequest(c.Method, f.Path+strings.ReplaceAll(c.URL, "@sourced", sourcedID), nil)
			bearer := "wrong"
			if c.Authorized {
				bearer = "synthetic-token"
			}
			req.Header.Set("Authorization", "Bearer "+bearer)
			supplied := "12345678-1234-4123-8123-123456789012"
			req.Header.Set("X-Request-ID", supplied)
			w := httptest.NewRecorder()
			app.ServeHTTP(w, req)
			returned := w.Header().Get("X-Request-ID")
			if returned == supplied || !uuid.MatchString(returned) {
				t.Fatalf("expected fresh UUID %q", returned)
			}
			if w.Code != c.Status || (w.Header().Get("Cache-Control") == "no-store") != c.PrivateHeaders {
				t.Fatalf("response %d %v %s, want %d private=%v", w.Code, w.Header(), w.Body, c.Status, c.PrivateHeaders)
			}
			if got := assertionVersion(t, ctx, db, id); got != c.After {
				t.Fatalf("after version %d want %d", got, c.After)
			}
			if c.Method == http.MethodHead {
				if w.Body.Len() != 0 {
					t.Fatalf("HEAD body %s", w.Body)
				}
				return
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if v, ok := body["requestId"]; ok {
				if v != returned {
					t.Fatalf("mismatched request ID %v", v)
				}
				body["requestId"] = "<request-id>"
			}
			rows, _ := body["revisions"].([]any)
			for _, row := range rows {
				v := row.(map[string]any)
				if ms, ok := v["recordedAt"].(string); ok && ms > "2024-01-01T00:00:00.000Z" {
					v["recordedAt"] = "<recorded-at>"
				}
			}
			if !reflect.DeepEqual(body, c.Body) {
				t.Fatalf("body %v want %v", body, c.Body)
			}
		})
	}
}
