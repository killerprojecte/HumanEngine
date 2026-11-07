package relations

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// PurgeInAssertionScope is the relation portion of projection removal and
// history cleanup. Other privacy owners still must perform their own effects.
// The owning assertion operation supplies the durable intent; relations do not
// invent a separate journal event or acquire another write/publication scope.
func (s *Service) PurgeInAssertionScope(store *assertions.Store, id string) error {
	return store.WithWriteSession(func(session *recovery.WriteSession) error {
		_, e := s.WithSession(session).PurgeAssertion(id)
		return e
	})
}

// AssertionCommitted matches CoordinatorOptions.Committed and binds the exact
// active child, including nested revision transactions.
func (s *Service) AssertionCommitted(store *assertions.Store, id string, version, epoch int64) error {
	return store.WithWriteSession(func(session *recovery.WriteSession) error {
		_, e := s.WithSession(session).AssertionChanged(id)
		return e
	})
}
