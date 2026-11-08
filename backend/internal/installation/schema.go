package installation

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var reportedColumns = []string{"reported_input_tokens", "reported_output_tokens", "reported_usage_calls", "cache_rate_input_tokens", "cache_rate_read_tokens", "cache_rate_known_calls"}
var usageColumns = []string{"reported_input_tokens", "reported_output_tokens", "reported_usage_calls", "cache_rate_input_tokens", "cache_rate_read_tokens", "cache_rate_known_calls", "ordinary_input_tokens", "cache_read_input_tokens", "cache_write_input_tokens", "cache_write_5m_input_tokens", "cache_write_1h_input_tokens", "cache_write_30m_input_tokens", "cache_write_12h_input_tokens", "cache_known_calls", "cache_read_known_calls", "cache_write_known_calls", "partial_priced_calls", "legacy_calls", "unknown_usage_calls", "ordinary_input_usd", "output_usd", "cache_read_usd", "cache_write_usd", "legacy_estimated_usd"}

const initialSchema = `
      CREATE TABLE IF NOT EXISTS model_usage (day TEXT NOT NULL, model_id TEXT NOT NULL, provider_id TEXT NOT NULL, api_model TEXT NOT NULL, role TEXT NOT NULL, calls INTEGER NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, duration_ms REAL NOT NULL, priced_calls INTEGER NOT NULL, estimated_usd REAL NOT NULL, PRIMARY KEY(day,model_id,provider_id,api_model,role));
      CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS scenes (id TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS recall_tombstones (scene_id TEXT NOT NULL, message_id TEXT NOT NULL, recalled_at TEXT NOT NULL, PRIMARY KEY(scene_id,message_id));
    `
const modelCallsSchema = `CREATE TABLE IF NOT EXISTS model_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, model_id TEXT NOT NULL,
    provider_id TEXT NOT NULL, api_model TEXT NOT NULL, role TEXT NOT NULL, status TEXT NOT NULL,
    attempt INTEGER NOT NULL, input_tokens INTEGER, output_tokens INTEGER,
    cache_read_tokens INTEGER, cache_write_tokens INTEGER, duration_ms REAL NOT NULL
  ); CREATE INDEX IF NOT EXISTS model_calls_at ON model_calls(at);
  CREATE INDEX IF NOT EXISTS model_calls_role_id ON model_calls(role,id);
  CREATE INDEX IF NOT EXISTS model_calls_model_id ON model_calls(model_id,id);`
const validUsage = "calls > 0 AND legacy_calls = 0 AND unknown_usage_calls = 0 AND input_tokens >= 0 AND output_tokens >= 0 AND (priced_calls + partial_priced_calls = calls OR cache_read_known_calls = calls OR cache_write_known_calls = calls OR cache_known_calls = calls)"
const pairedUsage = validUsage + " AND cache_read_known_calls = calls AND cache_read_input_tokens >= 0 AND cache_read_input_tokens <= input_tokens"
const usageKeys = "day,model_id,provider_id,api_model,role"

func columns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, e := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if e = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); e != nil {
			return nil, e
		}
		out[name] = true
	}
	return out, rows.Err()
}
func transaction(ctx context.Context, db *sql.DB, f func(*sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		e := tx.Rollback()
		if !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, e)
		}
	}()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func reportBackfill(ctx context.Context, tx *sql.Tx, table string) error {
	_, err := tx.ExecContext(ctx, "UPDATE "+table+" SET reported_input_tokens = CASE WHEN "+validUsage+" THEN input_tokens ELSE 0 END, reported_output_tokens = CASE WHEN "+validUsage+" THEN output_tokens ELSE 0 END, reported_usage_calls = CASE WHEN "+validUsage+" THEN calls ELSE 0 END, cache_rate_input_tokens = CASE WHEN "+pairedUsage+" THEN input_tokens ELSE 0 END, cache_rate_read_tokens = CASE WHEN "+pairedUsage+" THEN cache_read_input_tokens ELSE 0 END, cache_rate_known_calls = CASE WHEN "+pairedUsage+" THEN calls ELSE 0 END")
	return err
}

// InitializeStoreSchema preserves the source constructor's transaction boundaries,
// one-time history snapshot, cache cohorts and additive model-call migrations.
// It is writable startup only, after gate, secrets and real vector initialization.
func InitializeStoreSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, initialSchema); err != nil {
		return err
	}
	if err := transaction(ctx, db, func(tx *sql.Tx) error {
		cols, err := columns(ctx, tx, "model_usage")
		if err != nil {
			return err
		}
		for _, col := range usageColumns {
			if !cols[col] {
				typ := "INTEGER"
				if strings.HasSuffix(col, "_usd") {
					typ = "REAL"
				}
				if _, err = tx.ExecContext(ctx, "ALTER TABLE model_usage ADD COLUMN "+col+" "+typ+" NOT NULL DEFAULT 0"); err != nil {
					return err
				}
			}
		}
		for _, name := range []string{"cache_read_known_calls", "cache_write_known_calls"} {
			if !cols[name] {
				if _, err = tx.ExecContext(ctx, "UPDATE model_usage SET "+name+"=cache_known_calls"); err != nil {
					return err
				}
			}
		}
		if !cols["legacy_calls"] {
			if _, err = tx.ExecContext(ctx, "UPDATE model_usage SET legacy_calls=calls,legacy_estimated_usd=estimated_usd"); err != nil {
				return err
			}
		}
		if !cols["reported_usage_calls"] {
			return reportBackfill(ctx, tx, "model_usage")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := transaction(ctx, db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='model_usage_events')").Scan(&exists); err != nil {
			return err
		}
		if !exists {
			_, err := tx.ExecContext(ctx, `CREATE TABLE model_usage_history AS SELECT * FROM model_usage; CREATE UNIQUE INDEX model_usage_history_key ON model_usage_history(day,model_id,provider_id,api_model,role); CREATE TABLE model_usage_events AS SELECT *, '' AS at FROM model_usage WHERE 0; CREATE INDEX model_usage_events_at ON model_usage_events(at);`)
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if err := transaction(ctx, db, func(tx *sql.Tx) error {
		added := false
		for _, table := range []string{"model_usage", "model_usage_history", "model_usage_events"} {
			cols, err := columns(ctx, tx, table)
			if err != nil {
				return err
			}
			if table == "model_usage" || cols["reported_usage_calls"] {
				continue
			}
			added = true
			for _, col := range reportedColumns {
				if _, err = tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+col+" INTEGER NOT NULL DEFAULT 0"); err != nil {
					return err
				}
			}
			if err = reportBackfill(ctx, tx, table); err != nil {
				return err
			}
		}
		if added {
			sets, sums, preds := []string{}, []string{}, []string{}
			for _, c := range reportedColumns {
				sets = append(sets, c+" = source."+c)
				sums = append(sums, "SUM("+c+") AS "+c)
			}
			for _, k := range strings.Split(usageKeys, ",") {
				preds = append(preds, "target."+k+" = source."+k)
			}
			all := usageKeys + ",calls,input_tokens,output_tokens,duration_ms,priced_calls,estimated_usd," + strings.Join(usageColumns, ",")
			_, err := tx.ExecContext(ctx, "UPDATE model_usage AS target SET "+strings.Join(sets, ",")+" FROM (SELECT "+usageKeys+","+strings.Join(sums, ",")+" FROM (SELECT "+all+" FROM model_usage_history UNION ALL SELECT "+all+" FROM model_usage_events) GROUP BY "+usageKeys+") AS source WHERE "+strings.Join(preds, " AND "))
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, modelCallsSchema); err != nil {
		return err
	}
	if err := transaction(ctx, db, func(tx *sql.Tx) error {
		cols, err := columns(ctx, tx, "model_calls")
		if err != nil {
			return err
		}
		for _, col := range []struct{ name, definition string }{{"transport_mode", "TEXT NOT NULL DEFAULT 'unknown'"}, {"first_token_ms", "REAL"}, {"failure_reason", "TEXT"}, {"first_token_kind", "TEXT"}} {
			if !cols[col.name] {
				if _, err = tx.ExecContext(ctx, "ALTER TABLE model_calls ADD COLUMN "+col.name+" "+col.definition); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return transaction(ctx, db, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('source_event_heads','source_event_revisions','source_event_edit_keys','source_event_tombstones')").Scan(&count); err != nil {
			return err
		}
		if count != 4 {
			return nil
		}
		_, err := tx.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_source_event_heads_scene_message ON source_event_heads(scene_id,message_id)")
		return err
	})
}
