package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func TestBudgetAcknowledgementActualSource(t *testing.T) {
	var fixture struct {
		RestoreID, NextID string
		Cases             []struct {
			Name, Method, RequestBody, ContentType string
			Authorized                             bool
			Status                                 int
			Body                                   any
			Headers                                map[string]string
		}
	}
	raw, err := os.ReadFile("testdata/budget-ack-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) != 20 {
		t.Fatalf("source oracle: %d %v", len(fixture.Cases), err)
	}
	options := applicationOptions(t)
	options.Entries = []string{}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	a, err := Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	db, err := sql.Open("sqlite3", options.Installation.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	gate, err := recovery.ReadGate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	exec("CREATE TABLE fixture_ack_updates(count INTEGER); INSERT INTO fixture_ack_updates VALUES(0)")
	count, expectedState := 0, ""
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Name {
			case "stale":
				exec("CREATE TABLE recovery_model_hold(id INTEGER PRIMARY KEY CHECK(id=1),restore_id TEXT NOT NULL,status TEXT NOT NULL)")
				exec("INSERT INTO recovery_model_hold VALUES(1,?,'held')", fixture.RestoreID)
				exec("CREATE TRIGGER fixture_count_ack AFTER UPDATE OF status ON recovery_model_hold WHEN new.status='manually_acknowledged' BEGIN UPDATE fixture_ack_updates SET count=count+1; END")
				expectedState = "held"
			case "acknowledged", "fresh-hold-acknowledged":
				count++
				expectedState = "manually_acknowledged"
			case "fresh-hold-rejects-old-id":
				exec("UPDATE recovery_model_hold SET status='held',restore_id=?", fixture.NextID)
				expectedState = "held"
			case "malformed-state":
				exec("UPDATE recovery_model_hold SET status='not-canonical'")
				expectedState = "not-canonical"
			case "malformed-schema":
				exec("UPDATE recovery_model_hold SET status='held'; ALTER TABLE recovery_model_hold ADD COLUMN extra GENERATED ALWAYS AS ('bad') VIRTUAL")
				expectedState = "held"
			}
			requestCtx, stop := context.WithTimeout(ctx, 4*time.Second)
			defer stop()
			r := httptest.NewRequest(c.Method, "/api/budget/recovery/acknowledge", strings.NewReader(c.RequestBody)).WithContext(requestCtx)
			if c.ContentType != "" {
				r.Header.Set("Content-Type", c.ContentType)
			}
			const suppliedID = "12345678-1234-4123-8123-123456789012"
			r.Header.Set("X-Request-ID", suppliedID)
			sourceFixtureAuth(r, c.Authorized)
			res := httptest.NewRecorder()
			a.ServeHTTP(res, r)
			if res.Code != c.Status {
				t.Fatalf("HTTP %d: %s; want %d", res.Code, res.Body, c.Status)
			}
			for field, want := range c.Headers {
				if got := res.Header().Get(field); got != want {
					t.Errorf("%s: got %q; want %q", field, got, want)
				}
			}
			requestID := res.Header().Get("X-Request-ID")
			if len(requestID) != 36 || requestID == suppliedID {
				t.Fatalf("request ID was not freshly assigned: %q", requestID)
			}
			var body any
			if text := res.Body.String(); strings.HasPrefix(text, "{") {
				if err := json.Unmarshal([]byte(text), &body); err != nil {
					t.Fatal(err)
				}
				if p, ok := body.(map[string]any); ok && p["requestId"] != nil {
					if p["requestId"] != requestID {
						t.Fatal("uncorrelated failure response")
					}
					p["requestId"] = "<request-id>"
				}
			} else if text != "" {
				body = text
			}
			if !reflect.DeepEqual(body, c.Body) {
				t.Errorf("got %#v; want %#v", body, c.Body)
			}
			var persistedUpdates int
			if err := db.QueryRowContext(ctx, "SELECT count FROM fixture_ack_updates").Scan(&persistedUpdates); err != nil || persistedUpdates != count {
				t.Fatalf("unexpected hold updates: %d; want %d: %v", persistedUpdates, count, err)
			}
			if expectedState != "" {
				var state string
				if err := db.QueryRowContext(ctx, "SELECT status FROM recovery_model_hold").Scan(&state); err != nil || state != expectedState {
					t.Fatalf("hold state %q; want %q: %v", state, expectedState, err)
				}
			}
			if current, err := recovery.ReadGate(ctx, db); err != nil || current != gate {
				t.Fatalf("acknowledgement advanced the recovery gate: %+v %v", current, err)
			}
		})
	}
}
