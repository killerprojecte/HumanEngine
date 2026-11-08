package assertions

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// BorrowReadScope borrows one existing publication or write-child capability.
// Check validates lifetime/ownership; ready separately validates business
// readiness. Neither callback may acquire another publication or connection.
func BorrowReadScope(check, ready func() error, query func(string, ...any) (Rows, error), prepare func(string) (PreparedRead, error)) (*Reader, error) {
	if check == nil || ready == nil || query == nil || prepare == nil {
		return nil, errors.New("assertion_read_scope_required")
	}
	if err := check(); err != nil {
		return nil, err
	}
	r := &Reader{check: check}
	r.ready = func() error {
		if err := check(); err != nil {
			return err
		}
		return ready()
	}
	r.query = func(q string, args ...any) (Rows, error) {
		if err := check(); err != nil {
			return nil, err
		}
		return query(q, bind(args)...)
	}
	// Only native compiled handles are borrowed. The owner closes them at scope
	// exit; the support reader also releases its own handles when refresh ends.
	r.prepare = func(q string) (preparedRead, error) {
		if err := check(); err != nil {
			return nil, err
		}
		statement, err := prepare(q)
		if err != nil {
			return nil, err
		}
		return &borrowedNativeStatement{statement: statement, check: check}, nil
	}
	return r, nil
}

// PreparedRead is a native compiled query capability, never a cached result.
type PreparedRead = preparedRead

// CheckReadBinding rejects a store already bound to any different write child.
func (s *Store) CheckReadBinding(authority *recovery.Authority, main *sql.DB, expected *recovery.WriteSession) error {
	if s == nil || s.maintenance != nil || s.authority != authority || s.main != main {
		return errors.New("assertion_store_binding")
	}
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil {
		if s.session != expected {
			return errors.New("assertion_store_session_binding")
		}
		return s.requireTransaction()
	}
	return nil
}

// BorrowReader preserves this store's readiness policy inside an owner's
// existing publication. Owner identity must be checked by the composition.
func (s *Store) BorrowReader(check func() error, query func(string, ...any) (Rows, error), prepare func(string) (PreparedRead, error)) (*Reader, error) {
	if s.maintenance != nil {
		return nil, errors.New("assertion_live_session_required")
	}
	return BorrowReadScope(check, func() error {
		if s.options.AssertReady != nil {
			return s.options.AssertReady()
		}
		return nil
	}, query, prepare)
}

type borrowedNativeStatement struct {
	statement PreparedRead
	check     func() error
}

func (s *borrowedNativeStatement) Query(args ...any) (Rows, error) {
	if e := s.check(); e != nil {
		return nil, e
	}
	return s.statement.Query(bind(args)...)
}
func (s *borrowedNativeStatement) Close() error { return s.statement.Close() }
