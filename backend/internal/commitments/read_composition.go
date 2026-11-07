package commitments

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// ReadCurrent composes actual commitment and assertion readers within one
// publication, or the exact existing active write child. It does not initialize
// storage. The callback is synchronous, and its service expires on return.
// Capability checks precede source enabled short-circuiting; they are not
// additional source business readiness reads.
func (s *Store) ReadCurrent(ctx context.Context, assertionStore *assertions.Store, enabled func(Context) (bool, error), options assertionauthority.Options, fn func(*ReadService) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.maintenance != nil {
		return errors.New("commitment_live_session_required")
	}
	if fn == nil {
		return errors.New("commitment_read_operation_required")
	}
	if err := assertionStore.CheckReadBinding(s.authority, s.main, s.session); err != nil {
		return err
	}
	invoke := func(cr *Reader, ar *assertions.Reader) error {
		service, err := newReadService(cr, ar, enabled, options)
		if err != nil {
			return err
		}
		var active atomic.Bool
		active.Store(true)
		defer active.Store(false)
		check := service.check
		service.check = func() error {
			if !active.Load() {
				return sql.ErrTxDone
			}
			return check()
		}
		err = fn(service)
		return errors.Join(err, check())
	}
	if s.session != nil {
		if err := s.requireTransaction(); err != nil {
			return err
		}
		return assertionStore.WithSession(s.session).Read(ctx, func(ar *assertions.Reader) error {
			return ar.WithSupportReadStatements(func(scoped *assertions.Reader) error { return invoke(s.reader(), scoped) })
		})
	}
	return s.authority.WithWorkingSetRead(ctx, s.main, func(scope *recovery.WorkingSetReadSession) error {
		ar, err := assertionStore.BorrowReader(scope.CheckScope, func(q string, args ...any) (assertions.Rows, error) { return scope.Query(q, args...) }, publicationPrepare(scope))
		if err != nil {
			return err
		}
		cr, err := BorrowReadScope(scope.CheckScope, scope.CheckScope, func(q string, args ...any) (Rows, error) { return scope.Query(q, args...) }, s.options)
		if err != nil {
			return err
		}
		return ar.WithSupportReadStatements(func(scoped *assertions.Reader) error { return invoke(cr, scoped) })
	})
}

// publicationPrepare retains a real SQLite statement while keeping its native
// handle private to the recovery read scope.
func publicationPrepare(scope *recovery.WorkingSetReadSession) func(string) (assertions.PreparedRead, error) {
	return func(q string) (assertions.PreparedRead, error) {
		s, e := scope.PrepareQuery(q)
		if e != nil {
			return nil, e
		}
		return publicationStatement{s}, nil
	}
}

type publicationStatement struct {
	statement *recovery.WorkingSetReadStatement
}

func (s publicationStatement) Query(args ...any) (assertions.Rows, error) {
	return s.statement.Query(args...)
}
func (s publicationStatement) Close() error { return s.statement.Close() }
