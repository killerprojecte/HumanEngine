package usageview

import (
	"database/sql"
	"math"
	"testing"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

func TestUsageWriterAtomicDayCompactionAndTTL(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := installation.InitializeStoreSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	writer := NewWriter(db)
	at := time.Date(2026, 1, 1, 23, 59, 50, 0, time.UTC)
	writer.now = func() time.Time { return at }
	n := func(x float64) *float64 { return &x }
	call := modelservice.AttemptObservation{
		At: at.Add(-24 * time.Hour), Role: "planner", ModelID: "m", ProviderID: "p", APIModelName: "api",
		Completed: true, Usage: &modelservice.AttemptUsage{
			Known: true, CacheKnown: true, InputTokens: n(100), OutputTokens: n(5),
			CacheReadInputTokens: n(20), CacheWriteInputTokens: n(30), OrdinaryInputTokens: n(50),
			CacheWriteTTL: [4]*float64{n(5), n(6), n(7), n(8)},
		},
	}
	prices := map[string]float64{
		"inputUsdPerMillion": 2, "outputUsdPerMillion": 4, "cacheReadUsdPerMillion": 1,
		"cacheWriteUsdPerMillion": 3, "cacheWrite5mUsdPerMillion": 4, "cacheWrite1hUsdPerMillion": 5,
		"cacheWrite30mUsdPerMillion": 6, "cacheWrite12hUsdPerMillion": 7,
	}
	if err := writer.Record(ctx, call, prices); err != nil {
		t.Fatal(err)
	}
	var day, stamp string
	var write, ttl5, ttl1h, ttl30, ttl12h, priced int
	var usd float64
	if err := db.QueryRowContext(ctx, `SELECT day,at,cache_write_input_tokens,cache_write_5m_input_tokens,
cache_write_1h_input_tokens,cache_write_30m_input_tokens,cache_write_12h_input_tokens,priced_calls,cache_write_usd
FROM model_usage_events`).Scan(&day, &stamp, &write, &ttl5, &ttl1h, &ttl30, &ttl12h, &priced, &usd); err != nil {
		t.Fatal(err)
	}
	if day != "2026-01-01" || stamp != "2026-01-01T23:59:50.000Z" || write != 30 || ttl5 != 5 || ttl1h != 6 ||
		ttl30 != 7 || ttl12h != 8 || priced != 1 || math.Abs(usd-0.00016) > 1e-14 {
		t.Fatalf("sink day/TTL/price changed: %s %s %d %d %d %d %d %d %g", day, stamp, write, ttl5, ttl1h, ttl30, ttl12h, priced, usd)
	}
	at = time.Date(2026, 5, 1, 0, 0, 1, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_new_usage BEFORE INSERT ON model_usage_events
BEGIN SELECT raise(ABORT,'synthetic event insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	failure := call
	failure.Completed, failure.Usage = false, nil
	if err := writer.Record(ctx, failure, nil); err == nil || writer.compactedDay != "2026-01-01" {
		t.Fatalf("failed event classified as committed: %s %v", writer.compactedDay, err)
	}
	var totals, history, events int
	counts := func(wantTotals, wantHistory, wantEvents int) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT (SELECT sum(calls) FROM model_usage),
(SELECT count(*) FROM model_usage_history),(SELECT count(*) FROM model_usage_events)`).Scan(&totals, &history, &events); err != nil {
			t.Fatal(err)
		}
		if totals != wantTotals || history != wantHistory || events != wantEvents {
			t.Fatalf("totals/history/events = %d/%d/%d, want %d/%d/%d", totals, history, events, wantTotals, wantHistory, wantEvents)
		}
	}
	counts(1, 0, 1)
	if _, err := db.ExecContext(ctx, "DROP TRIGGER reject_new_usage"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Record(ctx, failure, nil); err != nil {
		t.Fatal(err)
	}
	counts(2, 1, 1)
	var unknown int
	if err := db.QueryRowContext(ctx, "SELECT unknown_usage_calls FROM model_usage_events").Scan(&unknown); err != nil || unknown != 1 {
		t.Fatalf("failed call unknown=%d: %v", unknown, err)
	}
}
