package memorymanual

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
)

type UpdateAcknowledgement struct {
	Reply, Stored *Object
	State         assertions.State
}

// UpdateForAcknowledgement samples revision state under the existing command
// writer. Nothing is exposed until that transaction commits successfully.
func (c *Command) UpdateForAcknowledgement(ctx context.Context, id string, patch *Object, guard *Guard) (ack UpdateAcknowledgement, err error) {
	if guard != nil {
		_, err = c.updateGuarded(ctx, id, patch, *guard, func(record *Object, state assertions.State) {
			ack = UpdateAcknowledgement{record, record, state}
		})
	} else {
		err = c.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
			record, err := c.update(ctx, tx, id, patch)
			if err != nil {
				return err
			}
			if err = tx.Checkpoint(); err != nil {
				return err
			}
			return tx.Store.Read(ctx, func(r *assertions.Reader) error {
				stored, err := r.Record(id)
				if err != nil {
					return err
				}
				state, err := r.State(id)
				ack = UpdateAcknowledgement{record, stored, state}
				return err
			})
		})
	}
	if err != nil {
		return UpdateAcknowledgement{}, err
	}
	return ack, nil
}
