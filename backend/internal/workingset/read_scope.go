package workingset

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
)

// ReadScope lends the authentic publication or exact active child to a composed
// domain reader. Scope queries, prepared queries and their rows expire with this
// subloan, even while the enclosing owner remains active. Close still permits
// cleanup. WriteSession is the existing parent capability, not a subloan, and is
// deliberately unchanged; callers must not re-enter publication.
func (s *Service) ReadScope(ctx context.Context, fn func(*Scope) error) error {
	if fn == nil {
		return errors.New("working_set_callback_required")
	}
	return s.Read(ctx, func(b *Service) error {
		if e := b.ready(); e != nil {
			return e
		}
		owner := b.scope()
		scope := *owner
		var active atomic.Bool
		active.Store(true)
		defer active.Store(false)
		scope.check = func() error {
			if !active.Load() {
				return sql.ErrTxDone
			}
			return owner.CheckScope()
		}
		scope.query = func(q string, args ...any) (Rows, error) {
			if e := scope.CheckScope(); e != nil {
				return nil, e
			}
			rows, e := owner.Query(q, args...)
			if e != nil {
				return nil, e
			}
			return loanRows{rows: rows, check: scope.CheckScope}, nil
		}
		scope.prepare = func(q string) (PreparedRead, error) {
			if e := scope.CheckScope(); e != nil {
				return nil, e
			}
			statement, e := owner.PrepareQuery(q)
			if e != nil {
				return nil, e
			}
			return loanStatement{statement: statement, check: scope.CheckScope}, nil
		}
		return errors.Join(fn(&scope), scope.CheckScope())
	})
}

// These wrappers guard every read operation, not just capability creation.
// Native methods retain their own owner/read-only/suspension checks as well.
type loanStatement struct {
	statement PreparedRead
	check     func() error
}

func (s loanStatement) Query(args ...any) (Rows, error) {
	if e := s.check(); e != nil {
		return nil, e
	}
	rows, e := s.statement.Query(args...)
	if e != nil {
		return nil, e
	}
	return loanRows{rows: rows, check: s.check}, nil
}
func (s loanStatement) Close() error { return s.statement.Close() }

type loanRows struct {
	rows  Rows
	check func() error
}

func (r loanRows) Next() bool {
	if r.check() != nil {
		return false
	}
	return r.rows.Next()
}
func (r loanRows) Scan(dest ...any) error {
	if e := r.check(); e != nil {
		return e
	}
	return r.rows.Scan(dest...)
}
func (r loanRows) Err() error {
	if e := r.check(); e != nil {
		return e
	}
	return r.rows.Err()
}
func (r loanRows) Close() error { return r.rows.Close() }
