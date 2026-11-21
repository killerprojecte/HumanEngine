package usageview

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type Store struct {
	Main  *sql.DB
	Dates *jsdate.Parser
}

func (s *Store) OwnedBy(db *sql.DB) bool { return s != nil && s.Main == db && s.Dates != nil }

type Result struct {
	Rows                          []any
	Incomplete, PlannerIncomplete bool
}

const aggregateByDay = " GROUP BY day,model_id,provider_id,api_model,CASE role WHEN 'thinker' THEN 'replyer' ELSE role END ORDER BY day DESC,model_id"

var allTimeSQL, selectAggregates, rawUsageColumns, rawUsageAfterDay = usageQuerySQL()

func usageQuerySQL() (string, string, string, string) {
	const keys = "day,model_id,provider_id,api_model,role"
	totals := [][2]string{
		{"calls", "calls"}, {"input_tokens", "inputTokens"}, {"output_tokens", "outputTokens"},
		{"duration_ms", "durationMs"}, {"priced_calls", "pricedCalls"}, {"estimated_usd", "estimatedUsd"},
	}
	selectSQL := "SELECT day,model_id AS modelId,provider_id AS providerId,api_model AS apiModelName,CASE role WHEN 'thinker' THEN 'replyer' ELSE role END AS role"
	raw := keys
	for _, pair := range append(totals, columns...) {
		raw += "," + pair[0]
		selectSQL += ",SUM(" + pair[0] + ") AS " + pair[1]
	}
	return selectSQL + " FROM model_usage" + aggregateByDay, selectSQL, raw, strings.TrimPrefix(raw, "day,")
}

func (s *Store) QueryOn(ctx context.Context, c *sql.Conn, q *Range) (Result, error) {
	out := Result{Rows: []any{}}
	if s == nil || s.Dates == nil || c == nil {
		return out, errors.New("usage_reader_dependencies_invalid")
	}
	query := allTimeSQL
	args := []any{}
	if q != nil {
		args = append(args, sql.Named("start", q.Start), sql.Named("end", q.End))
		local := q.Zone != ""
		omit := q.Bucket == "hour" || (local && q.Zone != "UTC")
		day := "day"
		if local {
			day = "usage_bucket(at,@zone,@bucket) AS day"
			args = append(args, sql.Named("zone", q.Zone), sql.Named("bucket", q.Bucket))
		} else if q.Bucket == "hour" {
			day = "strftime('%Y-%m-%dT%H:00:00Z',at) AS day"
		}
		history := ""
		if omit {
			history = "0 AND "
		}
		query = selectAggregates + " FROM (SELECT " + day + "," + rawUsageAfterDay + " FROM model_usage_events WHERE at >= @start AND at < @end UNION ALL SELECT " + rawUsageColumns + " FROM model_usage_history WHERE " + history + "day >= substr(@start,1,10) AND day <= substr(@end,1,10) AND day || 'T00:00:00.000Z' >= @start AND strftime('%Y-%m-%dT00:00:00.000Z',day,'+1 day') <= @end)" + aggregateByDay
		incomplete := `SELECT EXISTS(SELECT 1 FROM model_usage_history WHERE (@role IS NULL OR role = @role) AND day >= substr(@start,1,10) AND day <= substr(@end,1,10) AND day || 'T00:00:00.000Z' < @end AND strftime('%Y-%m-%dT00:00:00.000Z',day,'+1 day') > @start AND (@hour = 1 OR day || 'T00:00:00.000Z' < @start OR strftime('%Y-%m-%dT00:00:00.000Z',day,'+1 day') > @end) LIMIT 1)`
		for i, role := range []any{nil, "planner"} {
			var missing bool
			if e := c.QueryRowContext(ctx, incomplete, sql.Named("start", q.Start), sql.Named("end", q.End), sql.Named("role", role), sql.Named("hour", omit)).Scan(&missing); e != nil {
				return out, e
			}
			if i == 0 {
				out.Incomplete = missing
			} else {
				out.PlannerIncomplete = missing
			}
		}
	}
	rows, e := c.QueryContext(ctx, query, args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	names, e := rows.Columns()
	if e != nil {
		return out, e
	}
	values := make([]any, len(names))
	dest := make([]any, len(values))
	for i := range values {
		dest[i] = &values[i]
	}
	for rows.Next() {
		if e = rows.Scan(dest...); e != nil {
			return out, e
		}
		row := new(jsjson.Object)
		for i, v := range values {
			switch n := v.(type) {
			case int64:
				v = float64(n)
			case []byte:
				v = string(n)
			}
			if e = row.Set(names[i], v); e != nil {
				return out, e
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out, rows.Err()
}
