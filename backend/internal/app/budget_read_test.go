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

	"github.com/killerprojecte/HumanEngine/backend/internal/externalcost"
)

func TestBudgetAdminReadActualSource(t *testing.T) {
	var fixture struct {
		Today, Older, RestoreID string
		Settings                map[string]any
		Attempts                []struct {
			ID, Day, Lane, State string
			Known, Held          int64
			Overrun, Unbounded   bool
		}
		Receipts []json.RawMessage
		Cases    []struct {
			Name, Route, Method string
			Authorized          bool
			Status              int
			Body                any
			Headers             map[string]string
		}
	}
	raw, err := os.ReadFile("testdata/budget-read-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) != 13 {
		t.Fatalf("source oracle: %d %v", len(fixture.Cases), err)
	}
	options := applicationOptions(t)
	options.Entries = []string{}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	a, err := Open(ctx, options)
	if a != nil {
		t.Cleanup(func() {
			if err := a.Shutdown(context.Background()); err != nil {
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
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	today, older := now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02")
	materialDates := strings.NewReplacer(fixture.Today, today, fixture.Older, older)
	normalizeDates := strings.NewReplacer(today, fixture.Today, older, fixture.Older)
	put := func(i int) {
		t.Helper()
		receipt, err := externalcost.Parse(materialDates.Replace(string(fixture.Receipts[i])))
		if err == nil {
			err = (externalcost.Storage{DB: db}).Put(ctx, receipt)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	paths := map[string]string{"coverage": "/api/budget", "recovery": "/api/budget/recovery"}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Name {
			case "spend-held-disabled":
				for _, row := range fixture.Attempts {
					p, err := json.Marshal(map[string]any{"id": row.ID, "requestId": row.ID, "attempt": 1})
					if err != nil {
						t.Fatal(err)
					}
					date := materialDates.Replace(row.Day)
					exec(`INSERT INTO budget_attempts
					    (id,payload_hash,payload,policy,boot_id,state,window_start,lane,reserved_nano,known_nano,held_nano,created_at,overrun,unbounded_exposure)
					    VALUES(?, '', ?, '{}', 'fixture', ?, ?, ?, 0, ?, ?, ?, ?, ?)`, row.ID, string(p), row.State, date, row.Lane, row.Known, row.Held, date+"T00:00:00Z", row.Overrun, row.Unbounded)
				}
				exec("CREATE TABLE recovery_model_hold(id INTEGER PRIMARY KEY,restore_id TEXT,status TEXT)")
				exec("INSERT INTO recovery_model_hold VALUES(1,?,'held')", fixture.RestoreID)
			case "restoration-acknowledged":
				exec("UPDATE recovery_model_hold SET status='manually_acknowledged'")
			case "external-known-unbounded":
				wire, err := json.Marshal(fixture.Settings)
				if err != nil {
					t.Fatal(err)
				}
				exec("INSERT INTO config(key,value) VALUES('settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(wire))
				exec("UPDATE budget_attempts SET unbounded_exposure=1 WHERE id='carry'")
				put(0)
				put(1)
				// The browser must use its publisher snapshot, not a ledger
				// transaction that would block behind this independent writer.
				conn, err := db.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
					t.Fatal(err)
				}
				defer conn.ExecContext(context.Background(), "ROLLBACK")
			case "external-unknown-overshadows-unbounded":
				put(2)
			case "malformed-restoration":
				exec("UPDATE recovery_model_hold SET status='malformed'")
			}
			requestCtx, stop := context.WithTimeout(ctx, 3*time.Second)
			defer stop()
			request := httptest.NewRequest(c.Method, paths[c.Route], nil).WithContext(requestCtx)
			const suppliedID = "12345678-1234-4123-8123-123456789012"
			request.Header.Set("X-Request-ID", suppliedID)
			sourceFixtureAuth(request, c.Authorized)
			r := httptest.NewRecorder()
			a.ServeHTTP(r, request)
			if r.Code != c.Status {
				t.Fatalf("got HTTP %d: %s; want %d", r.Code, r.Body, c.Status)
			}
			for k, want := range c.Headers {
				if got := r.Header().Get(k); got != want {
					t.Errorf("%s: got %q, want %q", k, got, want)
				}
			}
			requestID := r.Header().Get("X-Request-ID")
			if len(requestID) != 36 || requestID == suppliedID {
				t.Fatalf("request ID was not freshly assigned: %q", requestID)
			}
			var body any
			if r.Body.Len() != 0 {
				if err := json.Unmarshal([]byte(normalizeDates.Replace(r.Body.String())), &body); err != nil {
					t.Fatal(err)
				}
				if p, ok := body.(map[string]any); ok && p["requestId"] != nil {
					if p["requestId"] != requestID {
						t.Fatal("uncorrelated failure response")
					}
					p["requestId"] = "<request-id>"
				}
			}
			if !reflect.DeepEqual(body, c.Body) {
				t.Errorf("got %#v; want %#v", body, c.Body)
			}
		})
	}
}
