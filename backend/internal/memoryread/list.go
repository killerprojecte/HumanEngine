// Package memoryread implements the fixed authorized listing, not MemoryService
// lifecycle refresh or legacy revision capture. Callers must complete those
// distinct write phases before entering this read.
package memoryread

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
)

type Input = assertions.ListRead
type Service struct {
	reader  *assertions.Reader
	options assertionauthority.Options
}

// New borrows one existing verified read scope or active write child. No store,
// connection, SQL callback, or synthetic maintenance hook is accepted here.
func New(reader *assertions.Reader, options assertionauthority.Options) (*Service, error) {
	if reader == nil {
		return nil, errors.New("memory_list_reader_required")
	}
	if e := reader.CheckScope(); e != nil {
		return nil, e
	}
	return &Service{reader, options}, nil
}
func (s *Service) List(input Input) ([]*assertions.Object, error) {
	rows, e := s.reader.MemoryListCandidates(input)
	if e != nil {
		return nil, e
	}
	// The source simulation/person short circuit precedes authority construction.
	if input.Simulation && input.PersonID != "" {
		return rows, nil
	}
	out := []*assertions.Object{}
	e = s.reader.WithSupportReadStatements(func(r *assertions.Reader) error {
		a, e := assertionauthority.New(r, s.options)
		if e != nil {
			return e
		}
		eligible := []*assertions.Object{}
		for _, record := range rows {
			ok, e := a.Supported(record)
			if e != nil {
				return e
			}
			if !ok {
				continue
			}
			ok, e = a.CurrentApplicable(record)
			if e != nil {
				return e
			}
			if ok {
				eligible = append(eligible, record)
			}
		}
		// Source filter completes before any sanitize call, preserving error order.
		for _, record := range eligible {
			v, e := a.Sanitize(record)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}
