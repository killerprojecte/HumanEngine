package app

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"testing"
)

//go:embed testdata/memory-history-source.json
var memoryHistoryFixture []byte

func TestMemoryHistoryActualSource(t *testing.T) {
	var fixture struct {
		Node, Path           string
		Scenes, Records      []json.RawMessage
		Source               struct{ SceneID, MessageID string }
		SourcedInput, Update json.RawMessage
		Cases                []struct {
			Name, Target, Query, Method, Action string
			Authorized, PrivateHeaders          bool
			Status, Before, After               int
			Body                                map[string]any
		}
	}
	if err := json.Unmarshal(memoryHistoryFixture, &fixture); err != nil || fixture.Node != "v24.22.1" {
		t.Fatalf("invalid source fixture: %s: %v", fixture.Node, err)
	}
	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	for _, scene := range fixture.Scenes {
		value := memoryPatchObject(t, scene)
		id := field(value, "id").(string)
		if field(value, "kind") == "simulation" {
			if _, err := db.ExecContext(ctx, "INSERT INTO scenes(id,value) VALUES(?,?)", id, string(scene)); err != nil {
				t.Fatal(err)
			}
		} else {
			appRequest(t, app, http.MethodPut, "/api/scenes/"+id, scene)
		}
	}
	for _, raw := range fixture.Records {
		var r struct {
			ID, Content string
			Scope       struct {
				Kind, SceneID string
				PersonID      *string
			}
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		key := r.Scope.Kind
		if key == "scene" {
			key += ":" + r.Scope.SceneID
		}
		_, err := db.ExecContext(ctx, "INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)", r.ID, key, r.Scope.PersonID, r.Content, r.ID, string(raw))
		if err != nil {
			t.Fatal(err)
		}
	}
	memory := app.Semantic.Memory
	sourced, err := memory.Create(ctx, memoryPatchObject(t, fixture.SourcedInput))
	if err != nil {
		t.Fatal(err)
	}
	uuid := regexp.MustCompile(`^[0-9a-f-]{36}$`)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			id := c.Target
			if id == "sourced" {
				id = field(sourced, "id").(string)
			}
			switch c.Action {
			case "fill-window":
				for n := 2; n <= 201; n++ {
					patch := memoryPatchObject(t, []byte(fmt.Sprintf(`{"content":"Window %d"}`, n)))
					if _, err := memory.Update(ctx, id, patch); err != nil {
						t.Fatalf("retained version %d: %v", n, err)
					}
				}
			case "update":
				if _, err := memory.Update(ctx, id, memoryPatchObject(t, fixture.Update)); err != nil {
					t.Fatal(err)
				}
			case "recall":
				if _, err := memory.Recall(ctx, fixture.Source.SceneID, fixture.Source.MessageID); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if found, err := memory.Delete(ctx, id); err != nil || !found {
					t.Fatalf("delete: %t %v", found, err)
				}
			}
			if n := assertionVersion(t, ctx, db, id); n != c.Before {
				t.Fatalf("before version %d want %d", n, c.Before)
			}
			req := httptest.NewRequest(c.Method, fixture.Path+c.Query+id, nil)
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
			if n := assertionVersion(t, ctx, db, id); n != c.After {
				t.Fatalf("after version %d want %d", n, c.After)
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
			if v, exists := body["requestId"]; exists {
				if v != returned {
					t.Fatalf("mismatched request ID %v", v)
				}
				body["requestId"] = "<request-id>"
			}
			revisions, _ := body["revisions"].([]any)
			for _, row := range revisions {
				r := row.(map[string]any)
				if v, ok := r["recordedAt"].(string); ok && v > "2024-01-01T00:00:00.000Z" {
					r["recordedAt"] = "<recorded-at>"
				}
			}
			if c.Action == "fill-window" && len(revisions) > 0 {
				body["revisionCount"] = float64(len(revisions))
				body["revisions"] = []any{revisions[0], revisions[len(revisions)-1]}
			}
			if !reflect.DeepEqual(body, c.Body) {
				t.Fatalf("body %v want %v", body, c.Body)
			}
		})
	}
}

func assertionVersion(t *testing.T, ctx context.Context, db *sql.DB, id string) int {
	t.Helper()
	var version int
	if err := db.QueryRowContext(ctx, "SELECT version FROM memory_assertion_state WHERE assertion_id=?", id).Scan(&version); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return version
}
