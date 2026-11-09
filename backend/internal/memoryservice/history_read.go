package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
)

// prepareHistoricalAnchor is the sole potentially writing part of history
// reading. Scoped guard validation never calls it or starts a RevisionTxn.
func (s *Service) prepareHistoricalAnchor(ctx context.Context, c Context, id string) (bool, error) {
	if err := ready(ctx, s.d.Assertions); err != nil {
		return false, err
	}
	if c.Simulation {
		return false, nil
	}
	if err := s.RefreshSupport(ctx); err != nil {
		return false, err
	}
	allowed := false
	err := s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		return tx.Store.Read(ctx, func(r *assertions.Reader) error {
			current, err := historicalCurrent(r, s.d.Authority, c, id)
			if err != nil || current == nil {
				return err
			}
			allowed = true
			state, err := r.State(id)
			if err != nil {
				return err
			}
			if state.Version == 0 && state.DeletionEpoch == 0 {
				authority, err := assertionauthority.New(r, s.d.Authority)
				if err != nil {
					return err
				}
				proofs, err := authority.HistorySupports(id)
				if err != nil {
					return err
				}
				_, err = tx.Store.ImportLegacy(current, proofs)
				return err
			}
			return nil
		})
	})
	return allowed, err
}
func historicalCurrent(r *assertions.Reader, options assertionauthority.Options, c Context, id string) (*Object, error) {
	current, err := r.Record(id)
	if err != nil || current == nil {
		return nil, err
	}
	scope := object(value(current, "scope"))
	if text(scope, "kind") != "scene" || text(scope, "sceneId") != c.SceneID {
		return nil, nil
	}
	authority, err := assertionauthority.New(r, options)
	if err != nil {
		return nil, err
	}
	supported, err := authority.Supported(current)
	if err != nil || !supported {
		return nil, err
	}
	return current, nil
}
func (s *Service) historyForRead(r *assertions.Reader, c Context, id string) (*Object, error) {
	current, err := historicalCurrent(r, s.d.Authority, c, id)
	if err != nil || current == nil {
		return nil, err
	}
	authority, err := assertionauthority.New(r, s.d.Authority)
	if err != nil {
		return nil, err
	}
	state, err := r.State(id)
	if err != nil {
		return nil, err
	}
	key := "scene:" + c.SceneID
	limit := float64(201)
	retained, err := r.Revisions(id, key, &limit)
	if err != nil {
		return nil, err
	}
	count, err := r.RetainedCount(id, key)
	if err != nil {
		return nil, err
	}
	coverage := "complete"
	if count > 200 || int64(len(retained)) != min(count, int64(201)) {
		coverage = "limited"
	}
	retained = retained[max(0, len(retained)-200):]
	views, err := authority.RetainedHistoryViews(retained, false)
	if err != nil {
		return nil, err
	}
	revisions := []any{}
	for _, revision := range views {
		record := object(value(revision, "record"))
		scope := object(value(record, "scope"))
		if text(scope, "kind") != "scene" || text(scope, "sceneId") != c.SceneID || !assertionauthority.ParentScopeAllows(object(value(current, "scope")), scope) {
			continue
		}
		sources, ok := value(record, "sources").([]any)
		if !ok {
			return nil, errors.New("assertion_array_required")
		}
		recalled := false
		for _, raw := range sources {
			source := object(raw)
			gone, err := r.Recalled(text(source, "sceneId"), text(source, "messageId"))
			if err != nil {
				return nil, err
			}
			if gone {
				recalled = true
				break
			}
		}
		if recalled {
			continue
		}
		proofs, _ := value(revision, "supports").([]any)
		if text(record, "lineageStatus") == "verified" && len(proofs) == 0 {
			continue
		}
		record, err = authority.WithObservation(record, proofs)
		if err != nil {
			return nil, err
		}
		_ = revision.Set("record", record)
		revisions = append(revisions, revision)
	}
	return epObj("version", float64(state.Version), "deletionEpoch", float64(state.DeletionEpoch), "deleted", state.Deleted, "revisions", revisions, "coverage", coverage), nil
}

// HistoryFor is actual source history inspection, with legacy enrollment on
// normal reads and historical privacy filtering. It is not a model permission.
func (s *Service) HistoryFor(ctx context.Context, c Context, id string) (out *Object, err error) {
	allowed, err := s.prepareHistoricalAnchor(ctx, c, id)
	if err != nil || !allowed {
		return nil, err
	}
	err = s.d.Assertions.Read(ctx, func(r *assertions.Reader) error { var err error; out, err = s.historyForRead(r, c, id); return err })
	return
}
