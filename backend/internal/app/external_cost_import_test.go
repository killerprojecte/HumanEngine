package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/externalcost"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type costImportBarrier struct {
	io.Reader
	entered chan<- struct{}
	release <-chan struct{}
}

func (r *costImportBarrier) Read(p []byte) (int, error) {
	if r.entered != nil {
		r.entered <- struct{}{}
		<-r.release
		r.entered = nil
	}
	return r.Reader.Read(p)
}

func TestExternalCostImportActualSource(t *testing.T) {
	var fixture struct {
		Stamp  string
		Events []any
		Cases  []struct {
			Name, RequestBody, ContentType        string
			Authorized, Existing                  bool
			Status, Rows, Authority, Gate, Reader int
			Body                                  any
			Headers                               map[string]string
		}
	}
	raw, err := os.ReadFile("testdata/external-cost-import-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) != 22 {
		t.Fatalf("source oracle: %d %v", len(fixture.Cases), err)
	}
	options := applicationOptions(t)
	options.Entries = []string{}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
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
	initial, err := recovery.ReadGate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(initial.Authority, "authority.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE TABLE fixture_cost_gate_updates(n INTEGER); INSERT INTO fixture_cost_gate_updates VALUES(0); CREATE TRIGGER fixture_cost_count_gate AFTER UPDATE ON recovery_gate BEGIN UPDATE fixture_cost_gate_updates SET n=n+1; END")
	observed := map[string]string{}
	normalize := func(v any) {
		t.Helper()
		row, ok := v.(map[string]any)
		if !ok || row["receipt"] == nil {
			return
		}
		id := row["receipt"].(map[string]any)["id"].(string)
		stamp, _ := row["recordedAt"].(string)
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", stamp); err != nil {
			t.Fatalf("unexpected JS instant %q: %v", stamp, err)
		}
		if previous, exists := observed[id]; exists && previous != stamp {
			t.Fatalf("persisted timestamp changed for %s", id)
		}
		observed[id] = stamp
		row["recordedAt"] = fixture.Stamp
	}
	gateCount := 0
	var originalHash string
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Name {
			case "unsupported-object":
				exec("CREATE VIEW budget_external_cost_extra AS SELECT 1")
			case "unrelated-corruption-idempotent":
				exec("DROP VIEW budget_external_cost_extra")
				if err := db.QueryRowContext(ctx, "SELECT payload_hash FROM budget_external_cost_revisions WHERE id='unknown'").Scan(&originalHash); err != nil {
					t.Fatal(err)
				}
				exec("UPDATE budget_external_cost_revisions SET payload_hash='invalid' WHERE id='unknown'")
			case "post-intent-failure":
				exec("UPDATE budget_external_cost_revisions SET payload_hash=? WHERE id='unknown'", originalHash)
				exec("CREATE TRIGGER fixture_abort_cost BEFORE INSERT ON budget_external_cost_revisions WHEN new.id='failed' BEGIN SELECT RAISE(ABORT,'fixture_sql_failure'); END")
			}
			call := func(body io.Reader) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/api/budget/external-costs", body).WithContext(ctx)
				r.Header.Set("Content-Type", c.ContentType)
				sourceFixtureAuth(r, c.Authorized)
				w := httptest.NewRecorder()
				a.ServeHTTP(w, r)
				return w
			}
			var w *httptest.ResponseRecorder
			if c.Name == "first" {
				// Both requests pass readiness just before reading body, then race
				// for the single main writer on the same absent ID.
				entered, release := make(chan struct{}, 2), make(chan struct{})
				out := make(chan *httptest.ResponseRecorder, 2)
				for range 2 {
					go func() {
						out <- call(&costImportBarrier{strings.NewReader(c.RequestBody), entered, release})
					}()
				}
				timeout := time.NewTimer(5 * time.Second)
				defer timeout.Stop()
				for range 2 {
					select {
					case <-entered:
					case <-timeout.C:
						close(release)
						t.Fatal("both imports did not reach the body barrier")
					}
				}
				close(release)
				w = <-out
				other := <-out
				if other.Code != c.Status || other.Body.String() != w.Body.String() {
					t.Fatalf("concurrent identical import diverged: %d %s versus %d %s", w.Code, w.Body, other.Code, other.Body)
				}
			} else {
				w = call(strings.NewReader(c.RequestBody))
			}
			if w.Code != c.Status {
				t.Fatalf("HTTP %d: %s; want %d", w.Code, w.Body, c.Status)
			}
			for k, want := range c.Headers {
				if got := w.Header().Get(k); got != want {
					t.Errorf("%s=%q, want %q", k, got, want)
				}
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["requestId"] != nil {
				if body["requestId"] != w.Header().Get("X-Request-ID") {
					t.Fatal("failure request ID mismatch")
				}
				body["requestId"] = "<request-id>"
			}
			normalize(body)
			if !reflect.DeepEqual(body, c.Body) {
				t.Errorf("received %#v, want %#v", body, c.Body)
			}
			current, err := recovery.ReadGate(ctx, db)
			wantReader := max(initial.MinimumReader, int64(c.Reader)) // Go startup may already require an eligibility reader.
			if err != nil || current.Head.Sequence-initial.Head.Sequence != int64(c.Gate) || current.MinimumReader != wantReader {
				t.Fatalf("main gate %+v, expected source advancement %d and reader %d: %v", current, c.Gate, wantReader, err)
			}
			var exists bool
			if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE name='budget_external_cost_revisions')").Scan(&exists); err != nil || exists != c.Existing {
				t.Fatalf("lazy table exists=%t, want %t: %v", exists, c.Existing, err)
			}
			if exists {
				var n int
				if err := db.QueryRowContext(ctx, "SELECT count(*) FROM budget_external_cost_revisions").Scan(&n); err != nil || n != c.Rows {
					t.Fatalf("main rows %d, want %d: %v", n, c.Rows, err)
				}
			}
			var updates int
			if err := db.QueryRowContext(ctx, "SELECT n FROM fixture_cost_gate_updates").Scan(&updates); err != nil {
				t.Fatal(err)
			}
			if c.Name == "first" {
				gateCount++ // exact event acknowledgement
				if initial.MinimumReader < 10 {
					gateCount++ // optional operational-reader upgrade
				}
			}
			if c.Name == "minus-zero-first" || c.Name == "unknown" {
				gateCount++
			}
			if updates != gateCount {
				t.Errorf("gate received %d writes, want %d", updates, gateCount)
			}
			rows, err := ledger.QueryContext(ctx, "SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence", initial.Head.Sequence)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			n := 0
			for rows.Next() {
				var body string
				var event map[string]any
				if err := rows.Scan(&body); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(body), &event); err != nil {
					t.Fatal(err)
				}
				normalize(event["receipt"])
				if n >= len(fixture.Events) || !reflect.DeepEqual(event, fixture.Events[n]) {
					t.Fatalf("unexpected authority event %d: %#v", n, event)
				}
				n++
			}
			if err := rows.Err(); err != nil || n != c.Authority {
				t.Fatalf("authority events %d, want %d: %v", n, c.Authority, err)
			}
		})
	}
	// Feed the actual persisted post-append failure into the existing qualified
	// offline replay owner on a third, independently scoped synthetic database.
	rows, err := ledger.QueryContext(ctx, "SELECT payload FROM revocations ORDER BY sequence")
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		events = append(events, raw)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "humanengine.sqlite")
	staging, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()
	initial.State = "quarantine"
	initialWire, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.ExecContext(ctx, "CREATE TABLE recovery_gate(id INTEGER PRIMARY KEY,value TEXT NOT NULL); INSERT INTO recovery_gate VALUES(1,?)", string(initialWire)); err != nil {
		t.Fatal(err)
	}
	reader, err := recovery.Open(ctx, initial.Authority)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	conn, err := staging.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	maintenance, err := recovery.AdmitMaintenance(ctx, conn, path, reader)
	if err != nil {
		t.Fatal(err)
	}
	offline := externalcost.NewOfflineMaintenance(maintenance)
	if err := externalcost.ReplayOffline(ctx, offline, events); err != nil {
		t.Fatal(err)
	}
	replayed, err := offline.ReadStorage().Latest(ctx, "failed")
	if err != nil || replayed == nil || replayed.RecordedAt() != observed["failed"] {
		t.Fatalf("post-append receipt did not replay exactly: %v %v", replayed, err)
	}
	var stagedWire string
	if err := conn.QueryRowContext(ctx, "SELECT value FROM recovery_gate WHERE id=1").Scan(&stagedWire); err != nil {
		t.Fatal(err)
	}
	var stagedGate recovery.Gate
	if err := json.Unmarshal([]byte(stagedWire), &stagedGate); err != nil || stagedGate != initial {
		t.Fatalf("domain replay changed staged authority/readiness: %+v %v", stagedGate, err)
	}

}
