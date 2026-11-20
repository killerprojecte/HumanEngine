package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/externalcost"
)

func TestExternalCostListActualSource(t *testing.T) {
	var fixture struct {
		Receipts []json.RawMessage
		Cases    []struct {
			Name, Query, Method string
			Authorized          bool
			Status              int
			Body                any
			Headers             map[string]string
		}
	}
	data, err := os.ReadFile("testdata/external-cost-read-source.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil || len(fixture.Cases) != 15 {
		t.Fatalf("source oracle: %d %v", len(fixture.Cases), err)
	}
	options := applicationOptions(t)
	options.Entries = []string{}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Name {
			case "sort-all":
				storage := externalcost.Storage{DB: db}
				for _, raw := range fixture.Receipts {
					r, err := externalcost.Parse(string(raw))
					if err != nil {
						t.Fatal(err)
					}
					if err := storage.Put(ctx, r); err != nil {
						t.Fatal(err)
					}
				}
			case "eager-corruption":
				if _, err := db.ExecContext(ctx, "UPDATE budget_external_cost_revisions SET payload_hash='corrupt' WHERE id='aa'"); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest(c.Method, "/api/budget/external-costs"+c.Query, nil).WithContext(ctx)
			r.Header.Set("X-Request-ID", "12345678-1234-4123-8123-123456789012")
			sourceFixtureAuth(r, c.Authorized)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			if w.Code != c.Status {
				t.Fatalf("got %d: %s; want %d", w.Code, w.Body, c.Status)
			}
			for k, want := range c.Headers {
				if got := w.Header().Get(k); got != want {
					t.Errorf("header %s=%q want %q", k, got, want)
				}
			}
			var body any
			if w.Body.Len() != 0 {
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if p, ok := body.(map[string]any); ok && p["requestId"] != nil {
					if p["requestId"] != w.Header().Get("X-Request-ID") {
						t.Fatal("uncorrelated error response")
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
