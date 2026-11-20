package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestIdentityAdminWriteActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/identity-admin-write-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Node          string
		Scenes        []json.RawMessage
		MinimumReader int64
		Cases         []struct {
			Name, CacheControl, Nosniff         string
			Request                             json.RawMessage
			Raw, Type, State                    *string
			Auth                                *bool
			Status                              int
			Body                                map[string]any
			RequestID, Rollback                 bool
			Events                              []string
			HeadDelta, GateDelta, MinimumReader int64
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || fixture.Node != "v24.22.1" || len(fixture.Cases) != 39 {
		t.Fatalf("invalid identity command fixture: %s (%v)", fixture.Node, err)
	}
	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	exec := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, scene := range fixture.Scenes {
		value := memoryPatchObject(t, scene)
		id := field(value, "id").(string)
		if field(value, "kind") == "simulation" {
			exec(t, "INSERT INTO scenes(id,value) VALUES(?,?)", id, string(scene))
		} else {
			appRequest(t, app, http.MethodPut, "/api/scenes/"+id, scene)
		}
	}
	base, err := recovery.ReadGate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if base.MinimumReader < fixture.MinimumReader {
		t.Fatalf("initial reader capability %d below source floor %d", base.MinimumReader, fixture.MinimumReader)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(base.Authority, "authority.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	trace := regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	previous := base.Head.Sequence
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Rollback {
				exec(t, "CREATE TRIGGER synthetic_registry_failure BEFORE UPDATE ON memory_identity_registry BEGIN SELECT RAISE(ABORT,'synthetic_registry_failure'); END")
			}
			body := c.Request
			if c.Raw != nil {
				body = []byte(*c.Raw)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/memory-identities", bytes.NewReader(body))
			sourceFixtureAuth(req, c.Auth == nil || *c.Auth)
			req.Header.Set("X-Request-ID", "fixture-spoof")
			contentType := "application/json"
			if c.Type != nil {
				contentType = *c.Type
			}
			req.Header.Set("Content-Type", contentType)
			res := httptest.NewRecorder()
			app.ServeHTTP(res, req)
			if res.Code != c.Status || res.Header().Get("Cache-Control") != c.CacheControl ||
				res.Header().Get("X-Content-Type-Options") != c.Nosniff || !trace.MatchString(res.Header().Get("X-Request-ID")) {
				t.Fatalf("got HTTP %d %v %s; want %d %v %v", res.Code, res.Header(), res.Body, c.Status, c.CacheControl, c.Nosniff)
			}
			var value map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			if id, ok := value["requestId"]; ok != c.RequestID || ok && id != res.Header().Get("X-Request-ID") {
				t.Fatalf("unexpected body trace: %v", id)
			}
			delete(value, "requestId")
			if !reflect.DeepEqual(value, c.Body) {
				t.Fatalf("body %v; want %v", value, c.Body)
			}
			var exists bool
			if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='memory_identity_registry')").Scan(&exists); err != nil || exists != (c.State != nil) {
				t.Fatalf("registry table: %t, want %t (%v)", exists, c.State != nil, err)
			}
			if exists {
				var state string
				if err := db.QueryRowContext(ctx, "SELECT value FROM memory_identity_registry WHERE id=1").Scan(&state); err != nil || state != *c.State {
					t.Fatalf("registry state: %s, want %s (%v)", state, *c.State, err)
				}
			}
			rows, err := ledger.QueryContext(ctx, "SELECT sequence,payload FROM revocations WHERE sequence>? ORDER BY sequence", previous)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			events := []string{}
			for rows.Next() {
				var event string
				if err := rows.Scan(&previous, &event); err != nil {
					t.Fatal(err)
				}
				events = append(events, event)
			}
			if err := rows.Err(); err != nil || !reflect.DeepEqual(events, c.Events) || previous != base.Head.Sequence+c.HeadDelta {
				t.Fatalf("authority events %v, want %v (%v)", events, c.Events, err)
			}
			gate, err := recovery.ReadGate(ctx, db)
			if err != nil || gate.Head.Sequence != base.Head.Sequence+c.GateDelta || gate.MinimumReader != max(base.MinimumReader, c.MinimumReader) {
				t.Fatalf("main ACK %d reader %d, want %d/%d (%v)", gate.Head.Sequence, gate.MinimumReader, base.Head.Sequence+c.GateDelta, max(base.MinimumReader, c.MinimumReader), err)
			}
		})
		if t.Failed() {
			break
		}
	}
}
