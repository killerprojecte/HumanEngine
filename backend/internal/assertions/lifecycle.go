package assertions

import (
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// WithWriteSession is a trusted internal composition bridge for domain owners.
// It borrows this Store's exact active capability; it never opens a connection,
// acquires publication authority, or substitutes the current child for a stale
// ancestor. CheckBinding releases the session mutex before invoking fn. The
// session and any owner bound to it expire with the surrounding transaction.
// This is not a plugin/read-authority capability.
func (s *Store) WithWriteSession(fn func(*recovery.WriteSession) error) error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if s.maintenance != nil {
		return errors.New("assertion_live_session_required")
	}
	if fn == nil {
		return errors.New("assertion_write_operation_required")
	}
	e := fn(s.session)
	return errors.Join(e, s.requireTransaction())
}

// RemoveVector removes the current assertion's embedding on the same write
// capability. Projection/FTS, relations and derived privacy remain their owners'
// responsibilities; deleting an embedding is not a replacement for those writes.
func (s *Store) RemoveVector(id string) error {
	return s.exec("DELETE FROM memory_vectors WHERE memory_id=?", id)
}
