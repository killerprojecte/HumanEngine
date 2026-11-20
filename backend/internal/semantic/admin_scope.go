package semantic

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"

	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// BindAdminCurrent validates the existing publisher's ownership once. The query
// and readiness callbacks are borrowed only from its active commitment/graph
// publication; no new connection, grants, or writer can be obtained here.
func (s *Store) BindAdminCurrent(publisher *recovery.ReadPublisher) (func(func() error, commitments.PublicationQuery, ModelReadPolicy, func(*ModelReader) error) error, error) {
	if s.bindingError != nil || s.session != nil || s.maintenance != nil || !publisher.OwnedBy(s.main, s.authority) {
		return nil, errors.New("semantic_publication_binding")
	}
	return func(ready func() error, query commitments.PublicationQuery, p ModelReadPolicy, fn func(*ModelReader) error) error {
		return fn(&ModelReader{ready: ready, query: func(q string, args ...any) (sourceevents.Rows, error) {
			return query(q, args...)
		}, policy: p})
	}, nil
}
