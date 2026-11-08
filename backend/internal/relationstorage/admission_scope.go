package relationstorage

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// BindAdmissionSession borrows an unbound root or the exact active child. It
// never rebases a retained ancestor onto a different transaction scope.
func (s *Store) BindAdmissionSession(session *recovery.WriteSession) (*Store, error) {
	if s.bindingError != nil {
		return nil, s.bindingError
	}
	if s.session != nil && s.session != session {
		return nil, errors.New("relation_store_session_binding")
	}
	bound := s.WithSession(session)
	if err := bound.RequireTransaction(); err != nil {
		return nil, err
	}
	return bound, nil
}
