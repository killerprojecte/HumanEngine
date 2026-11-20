package memoryservice

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func presentAdminRevision(r *assertions.Reader, record *Object, id string) error {
	state, err := r.State(id)
	if err != nil {
		return err
	}
	revision, _ := jsjson.NewObject(
		jsjson.Member{Key: "version", Value: float64(state.Version)},
		jsjson.Member{Key: "deletionEpoch", Value: float64(state.DeletionEpoch)},
	)
	return record.Set("revision", revision)
}

// PublishCreated acknowledges the actual committed row under the same sealed
// read/publication owner as administrative reads.
func (a *AdminRead) PublishCreated(ctx context.Context, id string, emit func([]byte) error) error {
	return a.publish(ctx, func(r *assertions.Reader) ([]byte, error) {
		record, err := r.Record(id)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, errors.New("created_memory_unavailable")
		}
		if err = presentAdminRevision(r, record, id); err != nil {
			return nil, err
		}
		return jsjson.Marshal(record)
	}, emit)
}
