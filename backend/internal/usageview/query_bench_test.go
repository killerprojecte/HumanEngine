package usageview

import (
	"database/sql"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/installation"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func BenchmarkUsageQuery(b *testing.B) {
	ctx := b.Context()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := installation.InitializeStoreSchema(ctx, db); err != nil {
		b.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	dates, err := jsdate.New("UTC")
	if err != nil {
		b.Fatal(err)
	}
	store := Store{Main: db, Dates: dates}
	run := func(b *testing.B, q *Range, wantCount int) {
		b.Helper()
		b.ReportAllocs()
		for b.Loop() {
			page, err := store.QueryOn(ctx, conn, q)
			if err != nil || len(page.Rows) != wantCount {
				b.Fatalf("usage rows=%d, want %d: %v", len(page.Rows), wantCount, err)
			}
			if _, err := jsjson.Marshal(page.Rows); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("all-time-empty", func(b *testing.B) { run(b, nil, 0) })
	_, err = conn.ExecContext(ctx, `
WITH RECURSIVE days(i) AS (VALUES(0) UNION ALL SELECT i+1 FROM days WHERE i<55),
models(id) AS (VALUES('model-a'),('model-b'),('model-c'),('model-d')),
roles(role) AS (VALUES('planner'),('replyer'))
INSERT INTO model_usage(day,model_id,provider_id,api_model,role,calls,input_tokens,output_tokens,duration_ms,priced_calls,estimated_usd)
SELECT date('2026-01-01','+'||i||' days'),id,'fixture-provider',id,role,4,1200,80,600.5,4,0.0018
FROM days CROSS JOIN models CROSS JOIN roles;
INSERT INTO model_usage_history SELECT * FROM model_usage WHERE day<'2026-01-29';
WITH hours(hour) AS (VALUES('03'),('07'),('12'),('18'))
INSERT INTO model_usage_events(day,model_id,provider_id,api_model,role,calls,input_tokens,output_tokens,duration_ms,priced_calls,estimated_usd,at)
SELECT day,model_id,provider_id,api_model,role,1,300,20,150.125,1,0.00045,day||'T'||hour||':30:00.000Z'
FROM model_usage CROSS JOIN hours WHERE day>='2026-01-29';`)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("all-time-448-rows", func(b *testing.B) { run(b, nil, 448) })
	q := &Range{Start: "2026-01-22T00:00:00.000Z", End: "2026-02-05T00:00:00.000Z", Bucket: "day"}
	b.Run("range-112-rows", func(b *testing.B) { run(b, q, 112) })
}
