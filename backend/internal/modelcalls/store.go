// Package modelcalls persists model-attempt metadata, never message content.
package modelcalls

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"math"
	"strings"
	"time"
)

const RetentionDays = 30
const MaxRecords = 100000

type Usage struct {
	Known                                bool
	Input, Output, CacheRead, CacheWrite *float64
}
type Observation struct {
	At, ModelID, ProviderID, APIModel, Role, Event string
	Attempt                                        int
	Usage                                          *Usage
	DurationMS                                     float64
	TransportMode                                  string
	FirstTokenMS                                   *float64
	FirstTokenKind, FailureReason                  *string
}
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }
func token(u *Usage, v *float64) any {
	if u == nil || !u.Known || v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v > 9007199254740991 || math.Trunc(*v) != *v {
		return nil
	}
	return *v
}
func (s *Store) Record(ctx context.Context, e Observation) error {
	if e.Role == "models" {
		return nil
	}
	if e.Role == "thinker" {
		e.Role = "replyer"
	}
	if e.APIModel == "" {
		e.APIModel = "unknown"
	}
	if e.TransportMode == "" {
		e.TransportMode = "unknown"
	}
	duration := e.DurationMS
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 {
		duration = 0
	}
	status := "failed"
	if e.Event == "model.completed" {
		status = "completed"
	}
	var input, output, read, write, first, kind any
	if e.Usage != nil {
		input = token(e.Usage, e.Usage.Input)
		output = token(e.Usage, e.Usage.Output)
		read = token(e.Usage, e.Usage.CacheRead)
		write = token(e.Usage, e.Usage.CacheWrite)
	}
	if e.TransportMode == "stream" {
		kind = e.FirstTokenKind
		if e.FirstTokenMS != nil && !math.IsNaN(*e.FirstTokenMS) && !math.IsInf(*e.FirstTokenMS, 0) && *e.FirstTokenMS >= 0 {
			first = *e.FirstTokenMS
		}
	}
	for _, value := range []*string{&e.At, &e.ModelID, &e.ProviderID, &e.APIModel, &e.Role} {
		normalized, err := jsjson.UTF8Text(*value)
		if err != nil {
			return err
		}
		*value = normalized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO model_calls(at,model_id,provider_id,api_model,role,status,attempt,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,duration_ms,transport_mode,first_token_ms,first_token_kind,failure_reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.At, e.ModelID, e.ProviderID, e.APIModel, e.Role, status, e.Attempt, input, output, read, write, duration, e.TransportMode, first, kind, e.FailureReason)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM model_calls WHERE at < ?", s.cutoff()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM model_calls WHERE id <= (SELECT MAX(id) FROM model_calls)-?", MaxRecords); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) cutoff() string {
	return s.now().UTC().Add(-RetentionDays * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
}

type Query struct {
	Limit                 int
	BeforeID              *int64
	Role, ModelID, Status *string
}
type Row struct {
	ID              int64    `json:"id"`
	At              string   `json:"at"`
	ModelID         string   `json:"modelId"`
	ProviderID      string   `json:"providerId"`
	APIModel        string   `json:"apiModelName"`
	Role            string   `json:"role"`
	Status          string   `json:"status"`
	Attempt         int      `json:"attempt"`
	Input           *int64   `json:"inputTokens"`
	Output          *int64   `json:"outputTokens"`
	CacheRead       *int64   `json:"cacheReadInputTokens"`
	CacheWrite      *int64   `json:"cacheWriteInputTokens"`
	DurationMS      float64  `json:"durationMs"`
	TransportMode   string   `json:"transportMode"`
	FirstTokenMS    *float64 `json:"firstTokenMs"`
	FirstTokenKind  *string  `json:"firstTokenKind"`
	FailureReason   *string  `json:"failureReason"`
	TokensPerSecond *float64 `json:"tokensPerSecond"`
}
type Page struct {
	Items         []Row  `json:"items"`
	NextCursor    *int64 `json:"nextCursor"`
	RetentionDays int    `json:"retentionDays"`
	MaxRecords    int    `json:"maxRecords"`
}

func (q Query) Validate() error {
	if q.Limit < 1 || q.Limit > 100 {
		return errors.New("invalid_model_calls_limit")
	}
	if q.BeforeID != nil && (*q.BeforeID < 1 || *q.BeforeID > 9007199254740991) {
		return errors.New("invalid_model_calls_cursor")
	}
	if q.Role != nil {
		switch *q.Role {
		case "planner", "replyer", "learning", "vision", "embedding":
		default:
			return errors.New("invalid_model_calls_role")
		}
	}
	if q.ModelID != nil {
		n, err := jsjson.UTF16Len(*q.ModelID)
		if err != nil || n > 200 {
			return errors.New("invalid_model_calls_model")
		}
	}
	if q.Status != nil && *q.Status != "completed" && *q.Status != "failed" {
		return errors.New("invalid_model_calls_status")
	}
	return nil
}

// QueryOn uses the caller's actual publication snapshot, not another connection.
func (s *Store) QueryOn(ctx context.Context, conn *sql.Conn, q Query) (Page, error) {
	p := Page{Items: []Row{}, RetentionDays: RetentionDays, MaxRecords: MaxRecords}
	if err := q.Validate(); err != nil {
		return p, err
	}
	clauses := []string{"at >= ?"}
	args := []any{s.cutoff()}
	for _, filter := range []struct {
		column string
		value  *string
	}{{"role", q.Role}, {"model_id", q.ModelID}, {"status", q.Status}} {
		if filter.value != nil {
			clauses = append(clauses, filter.column+"=?")
			normalized, err := jsjson.UTF8Text(*filter.value)
			if err != nil {
				return p, err
			}
			args = append(args, normalized)
		}
	}
	if q.BeforeID != nil {
		clauses = append(clauses, "id < ?")
		args = append(args, *q.BeforeID)
	}
	args = append(args, q.Limit+1)
	rows, err := conn.QueryContext(ctx, `SELECT id,at,model_id,provider_id,api_model,role,status,attempt,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,duration_ms,transport_mode,first_token_ms,first_token_kind,failure_reason FROM model_calls WHERE `+strings.Join(clauses, " AND ")+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Row
		if err = rows.Scan(&r.ID, &r.At, &r.ModelID, &r.ProviderID, &r.APIModel, &r.Role, &r.Status, &r.Attempt, &r.Input, &r.Output, &r.CacheRead, &r.CacheWrite, &r.DurationMS, &r.TransportMode, &r.FirstTokenMS, &r.FirstTokenKind, &r.FailureReason); err != nil {
			return p, err
		}
		if len(p.Items) == q.Limit {
			id := p.Items[len(p.Items)-1].ID
			p.NextCursor = &id
			break
		}
		if r.Status == "completed" && r.Output != nil && r.DurationMS > 0 {
			n := float64(*r.Output) * 1000 / r.DurationMS
			r.TokensPerSecond = &n
		}
		p.Items = append(p.Items, r)
	}
	return p, rows.Err()
}

func (s *Store) OwnedBy(db *sql.DB) bool { return s != nil && s.db == db }
