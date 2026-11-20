package usageview

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
)

type Writer struct {
	db           *sql.DB
	now          func() time.Time
	mu           sync.Mutex
	compactedDay string
}

func NewWriter(db *sql.DB) *Writer { return &Writer{db: db, now: time.Now} }

var insertUsage, insertUsageEvent, compactUsage = func() (string, string, string) {
	const keys = "day,model_id,provider_id,api_model,role"
	values := []string{"calls", "input_tokens", "output_tokens", "duration_ms", "priced_calls", "estimated_usd"}
	for _, col := range columns {
		values = append(values, col[0])
	}
	all := keys + "," + strings.Join(values, ",")
	sums, updates := make([]string, len(values)), make([]string, len(values))
	for i, c := range values {
		sums[i] = "SUM(" + c + ")"
		updates[i] = c + "=" + c + "+excluded." + c
	}
	conflict := " ON CONFLICT(" + keys + ") DO UPDATE SET " + strings.Join(updates, ",")
	placeholders := strings.Repeat("?,", 4+len(values)) + "?"
	return "INSERT INTO model_usage (" + all + ") VALUES (" + placeholders + ")" + conflict,
		"INSERT INTO model_usage_events (" + all + ",at) VALUES (" + placeholders + ",?)",
		"INSERT INTO model_usage_history (" + all + ") SELECT " + keys + "," + strings.Join(sums, ",") +
			" FROM model_usage_events WHERE at < ? GROUP BY " + keys + conflict
}()

// Record receives one physical Policy result. Both accumulators commit
// together, timestamped at the sink like Store.recordModel. The shared writer
// serializes only database work and advances the daily compaction on COMMIT.
func (s *Writer) Record(ctx context.Context, e modelservice.AttemptObservation, prices map[string]float64) error {
	if e.Role == "models" {
		return nil
	}
	if e.Role == "thinker" {
		e.Role = "replyer"
	}
	if e.APIModelName == "" {
		e.APIModelName = "unknown"
	}
	for _, value := range []*string{&e.ModelID, &e.ProviderID, &e.APIModelName, &e.Role} {
		text, err := jsjson.UTF8Text(*value)
		if err != nil {
			return err
		}
		*value = text
	}
	at := s.now().UTC()
	day := at.Format(time.DateOnly)
	args := usageArguments(day, e, prices)
	stamp := at.Format("2006-01-02T15:04:05.000Z")

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, insertUsage, args...); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, insertUsageEvent, append(args, stamp)...); err != nil {
		return err
	}
	if s.compactedDay != day {
		cutoff := at.AddDate(0, 0, -90).Format(time.DateOnly)
		if _, err = tx.ExecContext(ctx, compactUsage, cutoff); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM model_usage_events WHERE at < ?", cutoff); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.compactedDay = day
	return nil
}
