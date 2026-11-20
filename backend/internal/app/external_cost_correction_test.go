package app

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestExternalCostCorrectionActualSource(t *testing.T) {
	var f struct {
		Seeded struct{ Receipt json.RawMessage }
		AtCap  json.RawMessage
		Events []map[string]any
		Cases  []struct {
			Name, RequestBody                     string
			Authorized, Existing                  bool
			Status, Rows, Authority, Gate, Reader int
			Body                                  map[string]any
			Headers                               map[string]string
		}
	}
	raw, err := os.ReadFile("testdata/external-cost-correction-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &f); err != nil || len(f.Cases) != 24 {
		t.Fatalf("oracle: %d %v", len(f.Cases), err)
	}
	options := applicationOptions(t)
	options.Entries = []string{}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
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
	base, err := recovery.ReadGate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := sql.Open("sqlite3", filepath.Join(base.Authority, "authority.sqlite")+"?mode=ro")
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
	exec("CREATE TABLE fixture_amend_updates(n INTEGER); INSERT INTO fixture_amend_updates VALUES(0); CREATE TRIGGER fixture_amend_count AFTER UPDATE ON recovery_gate BEGIN UPDATE fixture_amend_updates SET n=n+1; END")
	post := func(path, body string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		sourceFixtureAuth(req, auth)
		out := httptest.NewRecorder()
		a.ServeHTTP(out, req)
		return out
	}
	stamps := map[string]string{}
	normalize := func(t *testing.T, got, want map[string]any) {
		t.Helper()
		id, _ := want["correctionId"].(string)
		if id == "" {
			return
		}
		stamp, _ := got["recordedAt"].(string)
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", stamp); err != nil {
			t.Fatal(err)
		}
		if old, ok := stamps[id]; ok && old != stamp {
			t.Fatalf("timestamp changed for %q", id)
		}
		stamps[id], got["recordedAt"] = stamp, want["recordedAt"]
	}
	var oldHash string
	offset := 0
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Name {
			case "missing-receipt":
				if res := post("/api/budget/external-costs", string(f.Seeded.Receipt), true); res.Code != 200 {
					t.Fatalf("seed %d: %s", res.Code, res.Body)
				}
				base, err = recovery.ReadGate(ctx, db)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.QueryRowContext(ctx, "SELECT n FROM fixture_amend_updates").Scan(&offset); err != nil {
					t.Fatal(err)
				}
			case "source-revision-cap":
				r, err := externalcost.Parse(string(f.AtCap))
				if err == nil {
					err = (externalcost.Storage{DB: db}).Put(ctx, r)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "corrupt-named-correction":
				if err := db.QueryRowContext(ctx, "SELECT payload_hash FROM budget_external_cost_revisions WHERE correction_id='amend1'").Scan(&oldHash); err != nil {
					t.Fatal(err)
				}
				exec("UPDATE budget_external_cost_revisions SET payload_hash='invalid' WHERE correction_id='amend1'")
			case "post-intent-failure":
				exec("UPDATE budget_external_cost_revisions SET payload_hash=? WHERE correction_id='amend1'", oldHash)
				exec("CREATE TRIGGER fixture_abort_amend BEFORE INSERT ON budget_external_cost_revisions WHEN new.correction_id='fail' BEGIN SELECT RAISE(ABORT,'fixture_sql_failure'); END")
			}
			out := post("/api/budget/external-costs/corrections", c.RequestBody, c.Authorized)
			if out.Code != c.Status {
				t.Fatalf("HTTP %d: %s; want %d", out.Code, out.Body, c.Status)
			}
			for k, want := range c.Headers {
				if got := out.Header().Get(k); got != want {
					t.Errorf("%s=%q, want %q", k, got, want)
				}
			}
			var body map[string]any
			if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["requestId"] != nil {
				if body["requestId"] != out.Header().Get("X-Request-ID") {
					t.Fatal("request ID mismatch")
				}
				body["requestId"] = "<request-id>"
			}
			normalize(t, body, c.Body)
			if !reflect.DeepEqual(body, c.Body) {
				t.Errorf("response %#v, want %#v", body, c.Body)
			}
			g, err := recovery.ReadGate(ctx, db)
			if err != nil || g.Head.Sequence-base.Head.Sequence != int64(c.Gate) || g.MinimumReader != max(base.MinimumReader, int64(c.Reader)) {
				t.Fatalf("gate: %+v, %v; expected delta %d", g, err, c.Gate)
			}
			var writes, count int
			if err := db.QueryRowContext(ctx, "SELECT n FROM fixture_amend_updates").Scan(&writes); err != nil || writes-offset != c.Gate {
				t.Fatalf("gate writes %d (%d offset), want %d: %v", writes, offset, c.Gate, err)
			}
			var exists bool
			if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='budget_external_cost_revisions')").Scan(&exists); err != nil || exists != c.Existing {
				t.Fatalf("table exists %t, want %t: %v", exists, c.Existing, err)
			}
			if exists {
				if err := db.QueryRowContext(ctx, "SELECT count(*) FROM budget_external_cost_revisions").Scan(&count); err != nil || count != c.Rows {
					t.Fatalf("rows %d, want %d: %v", count, c.Rows, err)
				}
			}
			rows, err := ledger.QueryContext(ctx, "SELECT payload FROM revocations WHERE sequence>? ORDER BY sequence", base.Head.Sequence)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			n := 0
			for rows.Next() {
				var wire string
				var event map[string]any
				if err := rows.Scan(&wire); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(wire), &event); err != nil {
					t.Fatal(err)
				}
				if n >= len(f.Events) {
					t.Fatalf("unexpected event %s", wire)
				}
				want := f.Events[n]
				normalize(t, event["receipt"].(map[string]any), want["receipt"].(map[string]any))
				if !reflect.DeepEqual(event, want) {
					t.Fatalf("event %#v, want %#v", event, want)
				}
				n++
			}
			if err := rows.Err(); err != nil || n != c.Authority {
				t.Fatalf("events %d, want %d: %v", n, c.Authority, err)
			}
		})
	}
}
