package externalcost

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Domain reads accept native storage and guarded offline transaction rows.
// Keeping the tiny row contracts here avoids exposing native deferred stepping
// from a recovery capability while retaining ordinary database/sql callers.
type rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}
type errorScanner struct{ err error }

func (s errorScanner) Scan(...any) error { return s.err }
func (s Storage) query(ctx context.Context, q string, args ...any) (rows, error) {
	switch db := s.DB.(type) {
	case interface {
		QueryContext(context.Context, string, ...any) (*recovery.MaintenanceRows, error)
	}:
		return db.QueryContext(ctx, q, args...)
	case interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}:
		return db.QueryContext(ctx, q, args...)
	default:
		return nil, errors.New("external_cost_query_unsupported")
	}
}
func (s Storage) row(ctx context.Context, q string, args ...any) scanner {
	switch db := s.DB.(type) {
	case interface {
		QueryRowContext(context.Context, string, ...any) *recovery.MaintenanceRow
	}:
		return db.QueryRowContext(ctx, q, args...)
	case interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}:
		return db.QueryRowContext(ctx, q, args...)
	default:
		return errorScanner{errors.New("external_cost_query_unsupported")}
	}
}
