package memoryservice

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorymanual"
)

// PublishUpdated refuses to acknowledge a superseding write as this one.
// Equal revision alone is insufficient for changes that do not retain history.
func (a *AdminRead) PublishUpdated(ctx context.Context, id string, ack memorymanual.UpdateAcknowledgement, emit func([]byte) error) error {
	expected, err := jsjson.Stringify(ack.Stored)
	if err != nil {
		return err
	}
	return a.publish(ctx, func(r *assertions.Reader) ([]byte, error) {
		state, err := r.State(id)
		if err != nil {
			return nil, err
		}
		if state.Deleted || state != ack.State {
			return nil, errors.New("updated_memory_superseded")
		}
		record, err := r.Record(id)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, errors.New("updated_memory_superseded")
		}
		actual, err := jsjson.Stringify(record)
		if err != nil {
			return nil, err
		}
		if actual != expected {
			return nil, errors.New("updated_memory_superseded")
		}
		reply := ack.Reply.Clone()
		if err = setAdminRevision(reply, state); err != nil {
			return nil, err
		}
		return jsjson.Marshal(reply)
	}, emit)
}
