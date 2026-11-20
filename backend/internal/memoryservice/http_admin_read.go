package memoryservice

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type AdminRead struct {
	publish func(context.Context, func(*assertions.Reader) ([]byte, error), func([]byte) error) error
}

func (s *Service) BindAdminRead(publisher *recovery.ReadPublisher) (*AdminRead, error) {
	publish, err := s.d.Assertions.BindPublishedRead(publisher)
	if err != nil {
		return nil, err
	}
	return &AdminRead{publish: publish}, nil
}

// adminRecords preserves storage order and the source's eager map: all rows
// are decoded before filtering internal records and adding revision metadata.
func adminRecords(r *assertions.Reader) ([]any, error) {
	rows, err := r.All()
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, row := range rows {
		record, ok := row.(*Object)
		if !ok {
			return nil, errors.New("assertion_record_object_required")
		}
		if truthy(value(record, "internalUse")) {
			continue
		}
		id, ok := value(record, "id").(string)
		if !ok {
			return nil, errors.New("assertion_record_id_required")
		}
		if err := presentAdminRevision(r, record, id); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// PublishList materializes the admin-only body under one recovery publication.
func (a *AdminRead) PublishList(ctx context.Context, emit func([]byte) error) (bool, error) {
	return a.publishBody(ctx, func(r *assertions.Reader) ([]byte, error) {
		records, err := adminRecords(r)
		if err != nil {
			return nil, err
		}
		return jsjson.Marshal(records)
	}, emit)
}

func (a *AdminRead) publishBody(ctx context.Context, read func(*assertions.Reader) ([]byte, error), emit func([]byte) error) (readStarted bool, err error) {
	err = a.publish(ctx, func(r *assertions.Reader) ([]byte, error) {
		readStarted = true
		return read(r)
	}, emit)
	return
}
