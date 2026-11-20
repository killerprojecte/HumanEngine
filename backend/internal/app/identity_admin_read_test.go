package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func TestIdentityAdminReadActualSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/identity-admin-read-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Node  string
		Data  json.RawMessage
		Cases []struct {
			Name, Route, Method, Action string
			Auth, NoStore, Table        bool
			Status                      int
			Body                        map[string]any
		}
	}
	if err := json.Unmarshal(raw, &oracle); err != nil || oracle.Node != "v24.22.1" {
		t.Fatalf("invalid identity fixture %s: %v", oracle.Node, err)
	}
	app, db := memoryDeleteApplication(t, "synthetic-token")
	ctx := t.Context()
	for _, c := range oracle.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Action {
			case "seed":
				if _, err := db.ExecContext(ctx, "CREATE TABLE memory_identity_registry (id INTEGER PRIMARY KEY CHECK(id=1), value TEXT NOT NULL)"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, "INSERT INTO memory_identity_registry VALUES(1,?)", string(oracle.Data)); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if _, err := db.ExecContext(ctx, "DELETE FROM memory_identity_registry"); err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest(c.Method, c.Route, nil)
			sourceFixtureAuth(req, c.Auth)
			res := httptest.NewRecorder()
			app.ServeHTTP(res, req)
			if res.Code != c.Status || (res.Header().Get("Cache-Control") == "no-store") != c.NoStore {
				t.Fatalf("got HTTP %d %v: %s; want %d no-store=%t", res.Code, res.Header(), res.Body, c.Status, c.NoStore)
			}
			var table bool
			if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='memory_identity_registry')").Scan(&table); err != nil || table != c.Table {
				t.Fatalf("GET changed registry table presence: %t want %t (%v)", table, c.Table, err)
			}
			if c.Method == http.MethodHead {
				if res.Body.Len() != 0 {
					t.Fatalf("HEAD sent a body: %s", res.Body)
				}
				return
			}
			var body map[string]any
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if requestID, ok := body["requestId"]; ok {
				if requestID != res.Header().Get("X-Request-ID") {
					t.Fatalf("inconsistent request ID")
				}
				delete(body, "requestId")
			}
			if !reflect.DeepEqual(body, c.Body) {
				actual, _ := json.Marshal(body)
				expected, _ := json.Marshal(c.Body)
				t.Fatalf("got %s\nwant %s", actual, expected)
			}
		})
	}
}
