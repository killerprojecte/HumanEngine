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

func (s *Store) QueryOn(ctx context.Context, c *sql.Conn, q *Range) (Result, error) {
	out := Result{Rows: []any{}}
	if s == nil || s.Dates == nil || c == nil {
		return out, errors.New("usage_reader_dependencies_invalid")
	}
	keys := []string{"day", "model_id", "provider_id", "api_model", "role"}
	values := []string{"calls", "input_tokens", "output_tokens", "duration_ms", "priced_calls", "estimated_usd"}
	aliases := []string{"calls", "inputTokens", "outputTokens", "durationMs", "pricedCalls", "estimatedUsd"}
	for _, pair := range columns {
		values = append(values, pair[0])
		aliases = append(aliases, pair[1])
	}
	all := append(append([]string{}, keys...), values...)
	source := "model_usage"
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
		source = "(SELECT " + day + "," + strings.Join(all[1:], ",") + " FROM model_usage_events WHERE at >= @start AND at < @end UNION ALL SELECT " + strings.Join(all, ",") + " FROM model_usage_history WHERE " + history + "day >= substr(@start,1,10) AND day <= substr(@end,1,10) AND day || 'T00:00:00.000Z' >= @start AND strftime('%Y-%m-%dT00:00:00.000Z',day,'+1 day') <= @end)"
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
	selectSQL := "day,model_id AS modelId,provider_id AS providerId,api_model AS apiModelName,CASE role WHEN 'thinker' THEN 'replyer' ELSE role END AS role"
	for i, v := range values {
		selectSQL += ",SUM(" + v + ") AS " + aliases[i]
	}
	rows, e := c.QueryContext(ctx, "SELECT "+selectSQL+" FROM "+source+" GROUP BY day,model_id,provider_id,api_model,CASE role WHEN 'thinker' THEN 'replyer' ELSE role END ORDER BY day DESC,model_id", args...)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	names, e := rows.Columns()
	if e != nil {
		return out, e
	}
	for rows.Next() {
		values := make([]any, len(names))
		dest := make([]any, len(names))
		for i := range values {
			dest[i] = &values[i]
		}
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
