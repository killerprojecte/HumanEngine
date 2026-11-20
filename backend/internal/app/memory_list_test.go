package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func TestMemoryAdminListActualSource(t *testing.T) {
	var fixture struct {
		Records []struct {
			Key   string
			Value json.RawMessage
		}
		States []struct {
			ID                              string
			Version, DeletionEpoch, Deleted int
		}
		Cases []struct {
			Name, Method string
			Authorized   bool
			Status       int
			Body         any
			Headers      map[string]string
		}
	}
	data, err := os.ReadFile("testdata/memory-list-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil || len(fixture.Cases) != 7 {
		t.Fatalf("memory list oracle: %d %v", len(fixture.Cases), err)
	}
	options := applicationOptions(t)
	options.Entries = []string{}
	ctx := context.Background()
	app, err := Open(ctx, options)
	if app != nil {
		t.Cleanup(func() {
			if err := app.Shutdown(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", options.Installation.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insert := func(id, value string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO memories(id,scope_key,person_id,content,fingerprint,value)
			VALUES(?, 'scene:scene', NULL, 'synthetic', ?, ?)`, id, id, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range fixture.Cases {
		switch c.Name {
		case "all-statuses-storage-order":
			for _, row := range fixture.Records {
				insert(row.Key, string(row.Value))
			}
			for _, state := range fixture.States {
				_, err := db.Exec(`INSERT INTO memory_assertion_state(assertion_id,version,deletion_epoch,deleted)
					VALUES(?,?,?,?)`, state.ID, state.Version, state.DeletionEpoch, state.Deleted)
				if err != nil {
					t.Fatal(err)
				}
			}
		case "malformed-row":
			insert("malformed", "{")
		}
		t.Run(c.Name, func(t *testing.T) {
			req := httptest.NewRequest(c.Method, "/api/memories", nil)
			const suppliedID = "12345678-1234-4123-8123-123456789012"
			req.Header.Set("X-Request-ID", suppliedID)
			sourceFixtureAuth(req, c.Authorized)
			res := httptest.NewRecorder()
			app.ServeHTTP(res, req)
			if res.Code != c.Status {
				t.Fatalf("got %d %s, want %d", res.Code, res.Body, c.Status)
			}
			for header, want := range c.Headers {
				if got := res.Header().Get(header); got != want {
					t.Errorf("%s: got %q, want %q", header, got, want)
				}
			}
			requestID := res.Header().Get("X-Request-ID")
			if len(requestID) != 36 || requestID == suppliedID {
				t.Errorf("request ID was not newly assigned: %q", requestID)
			}
			var body any
			if res.Body.Len() > 0 {
				if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
			}
			if m, ok := body.(map[string]any); ok && m["requestId"] != nil {
				if m["requestId"] != requestID {
					t.Errorf("body request ID does not match header: %v", m["requestId"])
				}
				m["requestId"] = "<request-id>"
			}
			if !reflect.DeepEqual(body, c.Body) {
				t.Errorf("got %#v, want %#v", body, c.Body)
			}
		})
	}
}
