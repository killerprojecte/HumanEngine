package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/nodeicu"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/usageview"
)

func TestUsageActualSourceHTTP(t *testing.T) {
	raw, e := os.ReadFile("../usageview/testdata/http-source.json")
	if e != nil {
		t.Fatal(e)
	}
	var source struct {
		Schema string
		Seed   struct {
			Events, History []map[string]any
			Columns         []string
		}
		Cases []struct {
			Name, Path, Method, Text string
			Empty, BadAuth           bool
			Status                   int
			ResponseHeaders          map[string]*string
			QueryValues              map[string][]string
		}
	}
	if e = json.Unmarshal(raw, &source); e != nil {
		t.Fatal(e)
	}
	for _, c := range source.Cases {
		t.Run(c.Name, func(t *testing.T) {
			s, db, a, _, _ := configurationFixture(t)
			s.token = "synthetic-token"
			if _, e = db.Exec(source.Schema); e != nil {
				t.Fatal(e)
			}
			if !c.Empty {
				for table, items := range map[string][]map[string]any{"model_usage_events": source.Seed.Events, "model_usage_history": source.Seed.History} {
					for _, row := range items {
						keys, args := []string{}, []any{}
						for k, v := range row {
							keys = append(keys, k)
							args = append(args, v)
						}
						placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
						if _, e = db.Exec("INSERT INTO "+table+" ("+strings.Join(keys, ",")+") VALUES ("+placeholders+")", args...); e != nil {
							t.Fatal(e)
						}
					}
				}
				columns := source.Seed.Columns
				values := []string{}
				for _, k := range columns[5:] {
					values = append(values, "SUM("+k+")")
				}
				if _, e = db.Exec("INSERT INTO model_usage SELECT " + strings.Join(columns[:5], ",") + "," + strings.Join(values, ",") + " FROM (SELECT " + strings.Join(columns, ",") + " FROM model_usage_events UNION ALL SELECT " + strings.Join(columns, ",") + " FROM model_usage_history) GROUP BY " + strings.Join(columns[:5], ",")); e != nil {
					t.Fatal(e)
				}
			}
			dates, e := jsdate.New("UTC")
			if e != nil {
				t.Fatal(e)
			}
			publisher, e := recovery.NewReadPublisherWithInitializer(context.Background(), db, a, installation.UsageReadInitializer(dates))
			if e != nil {
				t.Fatal(e)
			}
			defer publisher.CloseAndWait(context.Background())
			if e = s.WithUsage(&usageview.Store{Main: db, Dates: dates}, publisher); e != nil {
				t.Fatal(e)
			}
			r := httptest.NewRequest(c.Method, c.Path, nil)
			if got := honoQueryValues(r.URL.RawQuery); !reflect.DeepEqual(map[string][]string(got), c.QueryValues) {
				t.Fatalf("raw query got %#v want %#v", got, c.QueryValues)
			}
			token := "synthetic-token"
			if c.BadAuth {
				token = "bad"
			}
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			// These source-success cases require pinned ICU calendar parts.
			// The default binary deliberately cannot substitute host tzdata.
			if !usageHTTPNativeICU && (c.Name == "dst-hour" || c.Name == "quarter-hour") {
				if c.Status != 200 || c.Text == "[]" {
					t.Fatal("named-zone fixture must retain its real source-success oracle")
				}
				query := honoQueryValues(r.URL.RawQuery)
				err := publisher.Publish(context.Background(), func(conn *sql.Conn) ([]byte, error) {
					var bucket string
					err := conn.QueryRowContext(context.Background(), "SELECT usage_bucket(?, ?, ?)", "2026-11-01T06:15:00.000Z", query.Get("timeZone"), query.Get("bucket")).Scan(&bucket)
					return nil, err
				}, func([]byte) error { t.Fatal("unavailable bucket must not publish"); return nil })
				if err == nil || !strings.Contains(err.Error(), nodeicu.ErrUnavailable.Error()) {
					t.Fatalf("expected actual SQLite ICU-unavailable error, got %v", err)
				}
				assertUsageICUUnavailable(t, w, false)
				head := r.Clone(context.Background())
				head.Method = "HEAD"
				headResult := httptest.NewRecorder()
				s.ServeHTTP(headResult, head)
				assertUsageICUUnavailable(t, headResult, true)
				return
			}
			if w.Code != c.Status || w.Body.String() != c.Text {
				t.Fatalf("got %d %s; source %d %s", w.Code, w.Body.String(), c.Status, c.Text)
			}
			for k, v := range c.ResponseHeaders {
				got := w.Header().Get(k)
				if v == nil {
					if got != "" {
						t.Errorf("unexpected %s=%q", k, got)
					}
				} else if got != *v {
					t.Errorf("%s=%q want%q", k, got, *v)
				}
			}
			if c.Name == "offset-day" {
				var wg sync.WaitGroup
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						q := r.Clone(context.Background())
						out := httptest.NewRecorder()
						s.ServeHTTP(out, q)
						if out.Code != 200 || out.Body.String() != c.Text {
							t.Errorf("concurrent query %d %s", out.Code, out.Body.String())
						}
					}()
				}
				wg.Wait()
				cancelled, cancel := context.WithCancel(context.Background())
				cancel()
				out := httptest.NewRecorder()
				s.ServeHTTP(out, r.Clone(cancelled))
				if out.Body.Len() != 0 {
					t.Fatal("cancelled publication")
				}
			}
		})
	}
}

// assertUsageICUUnavailable checks the unavailable build contract, not source parity.
func assertUsageICUUnavailable(t *testing.T, w *httptest.ResponseRecorder, head bool) {
	t.Helper()
	if w.Code != 503 {
		t.Fatalf("unavailable ICU status = %d, body %s", w.Code, w.Body.String())
	}
	for _, key := range []string{"X-Usage-Incomplete", "X-Usage-Planner-Incomplete"} {
		if w.Header().Get(key) != "" {
			t.Fatalf("partial success header %s", key)
		}
	}
	if head {
		if w.Body.Len() != 0 {
			t.Fatal("HEAD unavailable response has body")
		}
		return
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["error"] != "Request failed; inspect server diagnostics" || body["requestId"] == "" {
		t.Fatalf("unexpected unavailable response: %s", w.Body.String())
	}
}
