package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryread"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

func (s *offlineFacade) historySupports(st *assertions.Store, id string) (out []any, err error) {
	err = st.Read(context.Background(), func(r *assertions.Reader) error {
		a, e := assertionauthority.New(r, s.d.Authority)
		if e != nil {
			return e
		}
		out, e = a.HistorySupports(id)
		return e
	})
	return
}

// Retention is deliberately historical: it does not use current validity or
// domainSourceCurrent, which would erase history on source edits/expiry.
func (s *offlineFacade) retain(st *assertions.Store, record *Object) (ok bool, err error) {
	if text(record, "status") == "invalidated" || text(record, "lineageStatus") == "quarantined" {
		return false, nil
	}
	err = st.Read(context.Background(), func(r *assertions.Reader) error {
		sources, valid := value(record, "sources").([]any)
		if !valid {
			return errors.New("assertion_array_required")
		}
		for _, v := range sources {
			source := object(v)
			recalled, e := r.Recalled(text(source, "sceneId"), text(source, "messageId"))
			if e != nil {
				return e
			}
			if recalled {
				return nil
			}
		}
		ok = true
		return nil
	})
	return
}
func (s *offlineFacade) purgeRelations(st *assertions.Store, id string) error {
	ctx := s.ctx
	return st.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
		_, e := s.d.Relations.WithMaintenanceTx(ctx, ws).PurgeAssertion(id)
		return e
	})
}
func (s *offlineFacade) committed(st *assertions.Store, id string, version, epoch int64) error {
	ctx := s.ctx
	if e := s.purgeRelations(st, id); e != nil {
		return e
	}
	if e := s.projectionCommitted(st, id, version, epoch); e != nil {
		return e
	}
	var record *Object
	if e := st.Read(context.Background(), func(r *assertions.Reader) error { var e error; record, e = r.Record(id); return e }); e != nil {
		return e
	}
	scope := object(value(record, "scope"))
	if text(scope, "kind") != "scene" {
		return nil
	}
	scene := text(scope, "sceneId")
	if e := st.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
		_, e := s.mutations(ws).Committed(workingset.Context{SceneID: scene}, []workingset.Reference{{Kind: "assertion", ID: id, Version: float64(version), DeletionEpoch: float64(epoch)}})
		return e
	}); e != nil {
		return e
	}
	return s.notify(ctx, st, scene, Reference{id, version, epoch})
}
func (s *offlineFacade) owners() assertionlifecycle.Owners {
	ctx := s.ctx
	return assertionlifecycle.Owners{
		RemoveProjection: func(st *assertions.Store, id string) error {
			if e := s.purgeRelations(st, id); e != nil {
				return e
			}
			if e := s.removeProjection(st, id); e != nil {
				return e
			}
			return s.unavailable(st, id)
		},
		BeforePurgeHistory: func(st *assertions.Store, id string) error {
			if e := s.purgeRelations(st, id); e != nil {
				return e
			}
			return s.unavailable(st, id)
		},
		BeforeInvalidateRecord: func(st *assertions.Store, id string) error {
			if e := s.d.Knowledge.PurgeInMaintenanceAssertionScope(st, id); e != nil {
				return e
			}
			return st.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
				return s.d.Semantic.WithMaintenanceTx(ctx, ws).PurgeAssertion(id)
			})
		},
	}
}
func (s *offlineFacade) refresh(tx *assertions.RevisionTxn) error {
	p, e := assertionlifecycle.New(tx, s.owners(), s.d.Authority)
	if e != nil {
		return e
	}
	_, e = p.RefreshSupport()
	return e
}
func (s *offlineFacade) read(ctx context.Context, st *assertions.Store, input memoryread.Input) (out []*Object, err error) {
	err = st.Read(ctx, func(r *assertions.Reader) error {
		read, e := memoryread.New(r, s.d.Authority)
		if e != nil {
			return e
		}
		out, e = read.List(input)
		return e
	})
	if err != nil {
		return nil, err
	}
	return
}
