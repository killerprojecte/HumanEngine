package commitments

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// ReadActiveAssertionCurrent refuses rebinding an already bound owner to a
// different child, even when both children use the same database connection.
func (s *Store) ReadActiveAssertionCurrent(ctx context.Context, session *recovery.WriteSession, assertionsStore *assertions.Store, enabled func(Context) (bool, error), options assertionauthority.Options, fn func(*ReadService) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil && s.session != session {
		return errors.New("commitment_store_session_binding")
	}
	return s.WithSession(session).ReadCurrent(ctx, assertionsStore, enabled, options, fn)
}
