package commitments

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

// ReadWorkingSetCurrent borrows the real working-set publication or active write
// child. It never enters a second transaction or acquires a second connection.
// The service and prepared support statements expire on callback return.
func (s *Store) ReadWorkingSetCurrent(ctx context.Context, scope *workingset.Scope, assertionStore *assertions.Store, enabled func(Context) (bool, error), options assertionauthority.Options, fn func(*ReadService) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.maintenance != nil {
		return errors.New("commitment_live_session_required")
	}
	if fn == nil {
		return errors.New("commitment_read_operation_required")
	}
	if e := scope.CheckReadBinding(s.authority, s.main); e != nil {
		return e
	}
	session := scope.WriteSession()
	if s.session != nil && s.session != session {
		return errors.New("commitment_store_session_binding")
	}
	if e := assertionStore.CheckReadBinding(s.authority, s.main, session); e != nil {
		return e
	}
	if session != nil {
		return s.WithSession(session).ReadCurrent(ctx, assertionStore.WithSession(session), enabled, options, fn)
	}
	ready := scope.CheckScope
	ar, e := assertionStore.BorrowReader(scope.CheckScope, func(q string, args ...any) (assertions.Rows, error) { return scope.Query(q, args...) }, func(q string) (assertions.PreparedRead, error) {
		p, e := scope.PrepareQuery(q)
		if e != nil {
			return nil, e
		}
		return workingScopeStatement{p}, nil
	})
	if e != nil {
		return e
	}
	cr, e := BorrowReadScope(scope.CheckScope, ready, func(q string, args ...any) (Rows, error) { return scope.Query(q, args...) }, s.options)
	if e != nil {
		return e
	}
	return ar.WithSupportReadStatements(func(scoped *assertions.Reader) error {
		service, e := newReadService(cr, scoped, enabled, options)
		if e != nil {
			return e
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
		return errors.Join(fn(service), check())
	})
}

type workingScopeStatement struct{ p workingset.PreparedRead }

func (s workingScopeStatement) Query(args ...any) (assertions.Rows, error) { return s.p.Query(args...) }
func (s workingScopeStatement) Close() error                               { return s.p.Close() }
