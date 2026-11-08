package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/memorymanual"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// LearningRecord is an exact administrative lookup for the engine wrapper. It
// does not export a general administrative enumeration to model callers.
func (s *Service) LearningRecord(ctx context.Context, id string) (out *Object, err error) {
	err = s.d.Assertions.Read(ctx, func(r *assertions.Reader) error { var e error; out, e = r.Record(id); return e })
	return
}
func (s *Service) IndexableByIDs(ctx context.Context, ids []string) (out []*Object, err error) {
	if e := s.RefreshSupport(ctx); e != nil {
		return nil, e
	}
	return s.indexableByIDs(ctx, s.d.Assertions, ids)
}
func (s *Service) IndexableByIDsInSession(ctx context.Context, ws *recovery.WriteSession, ids []string) ([]*Object, error) {
	scoped := *s
	scoped.revisions = s.revisions.WithSession(ws)
	if e := scoped.RefreshSupport(ctx); e != nil {
		return nil, e
	}
	return s.indexableByIDs(ctx, s.d.Assertions.WithSession(ws), ids)
}
func (s *Service) indexableByIDs(ctx context.Context, st *assertions.Store, ids []string) (out []*Object, err error) {
	out = []*Object{}
	err = st.Read(ctx, func(r *assertions.Reader) error {
		a, e := assertionauthority.New(r, s.d.Authority)
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			record, e := r.Record(id)
			if e != nil {
				return e
			}
			if record == nil || truthy(value(record, "internalUse")) || (text(record, "status") != "active" && text(record, "status") != "disputed") || (text(record, "origin") != "manual" && text(record, "lineageStatus") != "verified") {
				continue
			}
			ok, e := a.Supported(record)
			if e != nil {
				return e
			}
			if ok {
				out = append(out, record)
			}
		}
		return nil
	})
	return
}
func (s *Service) IndexInSession(ctx context.Context, ws *recovery.WriteSession, id, space string, vector []float64) error {
	st := s.d.Assertions.WithSession(ws)
	return st.Read(ctx, func(r *assertions.Reader) error {
		sp, e := r.VectorSpace(space)
		if e != nil {
			return e
		}
		if sp == nil {
			return errors.New("vector_space_not_found")
		}
		bytes, e := EncodeVector(sp, vector)
		if e != nil {
			return e
		}
		record, e := r.Record(id)
		if e != nil {
			return e
		}
		if record == nil || (text(record, "status") != "active" && text(record, "status") != "disputed") {
			return errors.New("memory_not_indexable")
		}
		if !(text(record, "origin") == "manual" && value(record, "lineageStatus") == nil) {
			a, e := assertionauthority.New(r, s.d.Authority)
			if e != nil {
				return e
			}
			ok, e := a.Supported(record)
			if e != nil {
				return e
			}
			if !ok {
				return errors.New("memory_not_indexable")
			}
		}
		_, e = ws.Exec("INSERT INTO memory_vectors VALUES(?,?,?) ON CONFLICT(memory_id,space_id) DO UPDATE SET embedding=excluded.embedding", id, space, bytes)
		return e
	})
}

// PromotePublicGuarded keeps the fresh model-read fence and public promotion in
// the same writer root, preserving normal revision hooks and post-commit flush.
func (s *Service) PromotePublicGuarded(ctx context.Context, input Context, id string, guard func(*recovery.WriteSession) error) (out memorymanual.Result, err error) {
	if guard == nil {
		return out, errors.New("public_promotion_guard_required")
	}
	err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
			if e := guard(ws); e != nil {
				return e
			}
			copy := *s
			copy.revisions = s.revisions.WithSession(ws)
			command, e := copy.manualCommands(memorymanual.Runtime{})
			if e != nil {
				return e
			}
			out, e = command.PromotePublic(ctx, memorymanual.Context{SceneID: input.SceneID, Simulation: input.Simulation}, id)
			return e
		})
	})
	return
}
