package memoryservice

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// OwnedBy rejects a service whose assertion root belongs to another installation
// or is borrowed from an active/expired session. All privacy owners still bind
// their operations to this root capability through the existing lifecycle.
func (s *Service) OwnedBy(main *sql.DB, authority *recovery.Authority) bool {
	return s != nil && s.revisions != nil && s.d.Assertions.OwnedBy(main, authority)
}

// PublishDelete executes the actual privacy lifecycle once, including revision
// and projection callbacks. Post-commit cancellation, witness failure or output
// failure is never permission to retry deletion or report a rollback. emit must
// only enqueue immutable bytes; it must not perform transport I/O.
func (s *Service) PublishDelete(ctx context.Context, main *sql.DB, authority *recovery.Authority, id string, emit func([]byte) error) error {
	if !s.OwnedBy(main, authority) || emit == nil {
		return errors.New("memory_delete_publication_requires_root")
	}
	deleted := false
	witness, err := s.revisions.TransactionWithReadWitness(ctx, func(tx *assertions.RevisionTxn) error {
		var e error
		deleted, e = s.deletePrivacy(ctx, tx, id)
		return e
	})
	if err != nil {
		return err
	}
	body := []byte(`{"ok":false}`)
	if deleted {
		body = []byte(`{"ok":true}`)
	}
	return authority.Publish(ctx, main, func(conn *sql.Conn) ([]byte, error) {
		if e := witness.Verify(ctx, conn); e != nil {
			return nil, e
		}
		return body, nil
	}, emit)
}
