package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestMemoryWorkingSetPinActualSource(t *testing.T) {
	var fixture struct {
		Node            string
		Scenes, Records []json.RawMessage
		Cases           []struct {
			Name, RequestBody, ContentType string
			Authorized                     bool
			Status                         int
			Body                           map[string]any
			Headers                        map[string]string
			Events, Rows                   []any
			GateAdvance, Reader            int64
		}
	}
	raw, err := os.ReadFile("testdata/memory-working-set-pin-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" || len(fixture.Cases) != 28 {
		t.Fatalf("invalid source oracle: %d %v", len(fixture.Cases), err)
	}
	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	if err := app.Maintenance.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
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
	for _, raw := range fixture.Records {
		r := memoryPatchObject(t, raw)
		id := field(r, "id").(string)
		exec("INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value) VALUES(?,?,?,?,?,?)",
			id, "scene:scene", "person", field(r, "content"), id, string(raw))
	}
	flags := func(workingSet, episodes bool) {
		t.Helper()
		wire, err := jsjson.Marshal(appRequest(t, app, http.MethodGet, "/api/settings", nil))
		if err != nil {
			t.Fatal(err)
		}
		var setting map[string]any
		if err := json.Unmarshal(wire, &setting); err != nil {
			t.Fatal(err)
		}
		setting["memoryWorkingSetEnabled"], setting["memoryEpisodesEnabled"] = workingSet, episodes
		appRequest(t, app, http.MethodPut, "/api/settings", setting)
	}
	flags(true, false)
	page := appRequest(t, app, http.MethodGet, "/api/memory-working-set?sceneId=scene&includeCold=true", nil).(*jsjson.Object)
	rows := field(page, "items").([]any)
	keys := map[string]string{}
	for _, raw := range rows {
		item := raw.(*jsjson.Object)
		record := field(item, "record").(*jsjson.Object)
		access := field(item, "accessibility").(*jsjson.Object)
		id, key := field(record, "id").(string), field(access, "dependencyKey").(string)
		if len(key) != 64 {
			t.Fatalf("invalid GET selection %q: %q", id, key)
		}
		keys[id] = key
	}
	if len(keys) != 2 || keys["target"] == "" || keys["failed"] == "" {
		t.Fatalf("missing published selections: %v", keys)
	}
	gate := func() recovery.Gate {
		t.Helper()
		g, err := recovery.ReadGate(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	initial := gate()
	ledger, err := sql.Open("sqlite3", filepath.Join(initial.Authority, "authority.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	exec("CREATE TABLE fixture_pin_gate_updates(n INTEGER); INSERT INTO fixture_pin_gate_updates VALUES(0); CREATE TRIGGER fixture_pin_count_gate AFTER UPDATE ON recovery_gate BEGIN UPDATE fixture_pin_gate_updates SET n=n+1; END")
	toRequest := strings.NewReplacer("@target", keys["target"], "@failed", keys["failed"])
	toFixture := strings.NewReplacer(keys["target"], "@target", keys["failed"], "@failed")
	readRows := func(db *sql.DB, query string, args ...any) []any {
		t.Helper()
		items := []any{}
		r, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		for r.Next() {
			var raw string
			var out any
			if err := r.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(toFixture.Replace(raw)), &out); err != nil {
				t.Fatal(err)
			}
			items = append(items, out)
		}
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		return items
	}
	gateWrites := func() int64 {
		t.Helper()
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT n FROM fixture_pin_gate_updates").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Name {
			case "disabled-pin":
				flags(false, false)
			case "episodes-only-repin":
				flags(false, true)
			case "terminal-policy":
				deleted, err := app.Semantic.Memory.Delete(ctx, "target")
				if err != nil || !deleted {
					t.Fatalf("terminal setup: %t %v", deleted, err)
				}
			case "post-intent-failure":
				exec("CREATE TRIGGER fixture_abort_pin BEFORE INSERT ON memory_working_set_pins WHEN new.entity_id='failed' BEGIN SELECT RAISE(ABORT,'fixture_pin_failure'); END")
			}
			var before int64
			if err := ledger.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0) FROM revocations").Scan(&before); err != nil {
				t.Fatal(err)
			}
			beforeGate, beforeWrites := gate(), gateWrites()
			const given = "12345678-1234-4123-8123-123456789012"
			r := httptest.NewRequest(http.MethodPost, "/api/memory-working-set/pin", strings.NewReader(toRequest.Replace(c.RequestBody))).WithContext(ctx)
			r.Header.Set("X-Request-ID", given)
			if c.ContentType != "" {
				r.Header.Set("Content-Type", c.ContentType)
			}
			sourceFixtureAuth(r, c.Authorized)
			out := httptest.NewRecorder()
			app.ServeHTTP(out, r)
			if out.Code != c.Status {
				t.Fatalf("status %d body %s; want %d", out.Code, out.Body, c.Status)
			}
			for name, expected := range c.Headers {
				if actual := out.Header().Get(name); actual != expected {
					t.Errorf("%s: got %q want %q", name, actual, expected)
				}
			}
			requestID := out.Header().Get("X-Request-ID")
			if len(requestID) != 36 || requestID == given {
				t.Fatalf("untrusted request ID %q", requestID)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(toFixture.Replace(out.Body.String())), &body); err != nil {
				t.Fatal(err)
			}
			if body["requestId"] != nil {
				if body["requestId"] != requestID {
					t.Fatal("response request ID differs")
				}
				body["requestId"] = "<request-id>"
			}
			if !reflect.DeepEqual(body, c.Body) {
				t.Fatalf("response %+v; want %+v", body, c.Body)
			}
			if events := readRows(ledger, "SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence", before); !reflect.DeepEqual(events, c.Events) {
				t.Fatalf("durable events %+v; want %+v", events, c.Events)
			}
			if rows := readRows(db, "SELECT value FROM memory_working_set_pins ORDER BY scene_id,kind,entity_id"); !reflect.DeepEqual(rows, c.Rows) {
				t.Fatalf("main rows %+v; want %+v", rows, c.Rows)
			}
			after := gate()
			// The native installation may already require a later reader for
			// unrelated modules. Pin must not lower it or raise it past its own floor.
			reader := max(initial.MinimumReader, c.Reader)
			if advance, writes := after.Head.Sequence-beforeGate.Head.Sequence, gateWrites()-beforeWrites; advance != c.GateAdvance || writes != c.GateAdvance || after.MinimumReader != reader {
				t.Fatalf("main gate: sequence advance %d, writes %d, reader %d; want %d/%d/%d", advance, writes, after.MinimumReader, c.GateAdvance, c.GateAdvance, reader)
			}
		})
	}
}
