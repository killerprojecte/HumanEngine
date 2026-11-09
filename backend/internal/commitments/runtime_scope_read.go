package commitments

import (
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// ReadRuntimeCurrent composes current commitment authority inside the actual
// model/tool read scope. It acquires no root publication or writer. An unbound
// owner is required; the scope itself may borrow the active write child.
func (s *Store) ReadRuntimeCurrent(scope sourceevents.RuntimeReadScope, assertionStore *assertions.Store, enabled func(Context) (bool, error), options assertionauthority.Options, fn func(*ReadService) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.maintenance != nil || s.session != nil {
		return errors.New("commitment_unbound_runtime_owner_required")
	}
	if fn == nil {
		return errors.New("commitment_read_operation_required")
	}
	check := func() error { return scope.CheckBinding(s.main, s.authority) }
	if err := check(); err != nil {
		return err
	}
	if err := assertionStore.CheckReadBinding(s.authority, s.main, nil); err != nil {
		return err
	}
	ar, err := assertionStore.BorrowReader(check, func(q string, args ...any) (assertions.Rows, error) { return scope.Query(q, args...) }, func(q string) (assertions.PreparedRead, error) {
		p, err := scope.PrepareAssertionRead(q)
		if err != nil {
			return nil, err
		}
		return runtimeScopeStatement{p}, nil
	})
	if err != nil {
		return err
	}
	cr, err := BorrowReadScope(check, scope.Ready, func(q string, args ...any) (Rows, error) { return scope.Query(q, args...) }, s.options)
	if err != nil {
		return err
	}
	return ar.WithSupportReadStatements(func(scoped *assertions.Reader) error {
		service, err := newReadService(cr, scoped, enabled, options)
		if err != nil {
			return err
		}
		var active atomic.Bool
		active.Store(true)
		defer active.Store(false)
		prior := service.check
		service.check = func() error {
			if !active.Load() {
				return sql.ErrTxDone
			}
			return prior()
		}
		return errors.Join(fn(service), prior())
	})
}

type runtimeScopeStatement struct {
	p sourceevents.RuntimeReadStatement
}

func (s runtimeScopeStatement) Query(args ...any) (assertions.Rows, error) { return s.p.Query(args...) }
func (s runtimeScopeStatement) Close() error                               { return s.p.Close() }
