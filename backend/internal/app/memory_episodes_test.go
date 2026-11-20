package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

func TestMemoryEpisodesActualSource(t *testing.T) {
	type proposal struct{ Context, Candidate json.RawMessage }
	var fixture struct {
		Node                       string
		Scenes, Seeds, Packed      []json.RawMessage
		SourceProposal, Commitment proposal
		Cases                      []struct {
			Name, Query, Method string
			Auth                bool
			Status              int
			Body                map[string]any
			Headers             map[string]string
			Workspace           struct{ Items, Jobs, Legacy int }
		}
	}
	raw, err := os.ReadFile("testdata/memory-episodes-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) != 19 || fixture.Node != "v24.22.1" {
		t.Fatalf("invalid source oracle: %d %v", len(fixture.Cases), err)
	}
	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, scene := range fixture.Scenes {
		o := memoryPatchObject(t, scene)
		id := field(o, "id").(string)
		if field(o, "kind") == "simulation" {
			exec("INSERT INTO scenes(id,value) VALUES(?,?)", id, string(scene))
		} else {
			appRequest(t, app, http.MethodPut, "/api/scenes/"+id, scene)
		}
	}
	add := func(rows []json.RawMessage) {
		for _, row := range rows {
			var o struct {
				ID, Content string
				Scope       struct {
					Kind, SceneID string
					PersonID      *string
				}
			}
			if err := json.Unmarshal(row, &o); err != nil {
				t.Fatal(err)
			}
			scope := "shared:public"
			if o.Scope.Kind == "scene" {
				scope = "scene:" + o.Scope.SceneID
			}
			exec("INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)", o.ID, scope, o.Scope.PersonID, o.Content, o.ID, string(row))
		}
	}
	add(fixture.Seeds)
	flag := func(changes map[string]any) {
		settings := appRequest(t, app, http.MethodGet, "/api/settings", nil)
		wire, err := jsjson.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(wire, &object); err != nil {
			t.Fatal(err)
		}
		for k, v := range changes {
			object[k] = v
		}
		appRequest(t, app, http.MethodPut, "/api/settings", object)
	}
	memory := app.Semantic.Memory
	var learned string
	save := func(p proposal) memoryservice.ProposalResult {
		out, err := memory.Propose(ctx, memoryPatchObject(t, p.Context), memoryPatchObject(t, p.Candidate), memoryservice.ProposalOptions{})
		if err != nil || out.Status != "saved" {
			t.Fatalf("proposal: %+v %v", out, err)
		}
		return out
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Name {
			case "enabled-person":
				flag(map[string]any{"memoryEpisodesEnabled": true, "memoryCommitmentsEnabled": true, "memoryCommitmentsAnswerEnabled": false})
				out := save(fixture.SourceProposal)
				learned = field(out.Memory, "id").(string)
				save(fixture.Commitment)
			case "commitments-answer-on":
				flag(map[string]any{"memoryCommitmentsAnswerEnabled": true})
			case "after-recall":
				if _, err := memory.Recall(ctx, "scene", "episode-source"); err != nil {
					t.Fatal(err)
				}
			case "bounded-prefix":
				add(fixture.Packed)
			}
			req := httptest.NewRequest(c.Method, "/api/memory-episodes"+c.Query, nil).WithContext(ctx)
			const given = "12345678-1234-4123-8123-123456789012"
			req.Header.Set("X-Request-ID", given)
			sourceFixtureAuth(req, c.Auth)
			out := httptest.NewRecorder()
			app.ServeHTTP(out, req)
			if out.Code != c.Status {
				t.Fatalf("HTTP %d: %s; want %d", out.Code, out.Body, c.Status)
			}
			for key, want := range c.Headers {
				if got := out.Header().Get(key); got != want {
					t.Errorf("%s=%q want %q", key, got, want)
				}
			}
			requestID := out.Header().Get("X-Request-ID")
			if len(requestID) != 36 || requestID == given {
				t.Fatalf("invalid request ID %q", requestID)
			}
			var body map[string]any
			if out.Body.Len() != 0 {
				if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["requestId"] != nil {
					if body["requestId"] != requestID {
						t.Fatal("request ID mismatch")
					}
					body["requestId"] = "<request-id>"
				}
				items, _ := body["items"].([]any)
				for _, raw := range items {
					item := raw.(map[string]any)
					if r, ok := item["record"].(map[string]any); ok && r["id"] == learned {
						r["id"], r["learnedAt"] = "@learned", "<learned>"
						item["time"].(map[string]any)["recordedAt"] = "<learned>"
					}
					if plan, ok := item["view"].(map[string]any); ok && plan["recordedAt"] != nil {
						plan["recordedAt"] = "<recorded>"
					}
				}
			}
			if !reflect.DeepEqual(body, c.Body) {
				got, _ := json.Marshal(body)
				want, _ := json.Marshal(c.Body)
				t.Fatalf("body %s; want %s", got, want)
			}
			var legacy int
			if err := db.QueryRowContext(ctx, "SELECT version FROM memory_assertion_state WHERE assertion_id='a-A'").Scan(&legacy); err != nil && !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
			if legacy != c.Workspace.Legacy {
				t.Errorf("legacy state %d want %d", legacy, c.Workspace.Legacy)
			}
			for _, item := range []struct {
				name string
				want int
			}{{"memory_working_set_items", c.Workspace.Items}, {"memory_working_set_jobs", c.Workspace.Jobs}} {
				var present bool
				if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name=?)", item.name).Scan(&present); err != nil {
					t.Fatal(err)
				}
				var n int
				if present {
					if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+item.name).Scan(&n); err != nil {
						t.Fatal(err)
					}
				}
				if n != item.want {
					t.Errorf("%s=%d, want %d", item.name, n, item.want)
				}
			}
		})
	}
	t.Run("proof-changes-during-sync-refresh", func(t *testing.T) {
		add([]json.RawMessage{json.RawMessage(`{"id":"fixture-late","scope":{"kind":"scene","sceneId":"scene","personId":"fixture-late"},"status":"active","kind":"experience","content":"STALE_BODY","learnedAt":"2025-01-01T00:00:00.000Z","sources":[]}`)})
		// Synthetic interleave inside the final metadata write, after the
		// detached page was built and before its publication is admitted.
		exec(`CREATE TRIGGER fixture_episode_late_source AFTER INSERT ON memory_working_set_items
			WHEN new.entity_id='fixture-late' BEGIN UPDATE memories SET value=json_set(value,'$.content','CHANGED_BODY') WHERE id='fixture-late'; END`)
		r := httptest.NewRequest(http.MethodGet, "/api/memory-episodes?sceneId=scene&personId=fixture-late", nil).WithContext(ctx)
		sourceFixtureAuth(r, true)
		out := httptest.NewRecorder()
		app.ServeHTTP(out, r)
		if out.Code != http.StatusServiceUnavailable || strings.Contains(out.Body.String(), "STALE_BODY") || strings.Contains(out.Body.String(), "CHANGED_BODY") {
			t.Fatalf("changed proof crossed publication: %d %s", out.Code, out.Body)
		}
		var body string
		if err := db.QueryRowContext(ctx, "SELECT json_extract(value,'$.content') FROM memories WHERE id='fixture-late'").Scan(&body); err != nil || body != "CHANGED_BODY" {
			t.Fatalf("interleave did not occur: %q %v", body, err)
		}
	})

}
