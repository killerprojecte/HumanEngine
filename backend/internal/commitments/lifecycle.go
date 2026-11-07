package commitments

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// ReadPrivacyRefs enumerates detached exact refs using an already scoped reader.
// This is the inner source read, NOT validateFixedReads/prepared authorization.
// Root lifecycle composition must supply the stronger read-only capture scope.
func ReadPrivacyRefs(reader *assertions.Reader, options assertionauthority.Options) (refs []Reference, err error) {
	refs = []Reference{}
	if reader == nil {
		return nil, errors.New("commitment_assertion_reader_required")
	}
	err = reader.WithSupportReadStatements(func(r *assertions.Reader) error {
		a, e := assertionauthority.New(r, options)
		if e != nil {
			return e
		}
		records, e := r.All()
		if e != nil {
			return e
		}
		for _, record := range records {
			if get(get(record, "scope"), "kind") != "scene" {
				continue
			}
			id := str(get(record, "id"))
			state, e := r.State(id)
			if e != nil {
				return e
			}
			if state.Deleted || state.Version < 1 {
				continue
			}
			ref := Reference{ID: id, Version: float64(state.Version), DeletionEpoch: float64(state.DeletionEpoch)}
			view, e := a.RelationAuthority(assertionauthority.Context{SceneID: str(get(get(record, "scope"), "sceneId"))}, ref)
			if e != nil {
				return e
			}
			if view != nil {
				refs = append(refs, ref)
			}
		}
		return r.CheckScope()
	})
	return
}

// Refresh uses actual assertion history and privacy provenance after checkpoint.
// It never consults feature/learning flags and never starts a model. Every domain
// binding and callback uses this RevisionTxn's active child, not its ancestor.
func (s *PrivacyService) Refresh(ctx context.Context, tx *assertions.RevisionTxn, before []Reference, options assertionauthority.Options) error {
	if tx == nil || tx.Store == nil {
		return errors.New("commitment_revision_transaction_required")
	}
	if e := tx.Checkpoint(); e != nil {
		return e
	}
	return tx.Store.WithWriteSession(func(session *recovery.WriteSession) error {
		bound := s.WithSession(session)
		if e := bound.Store.ready(); e != nil {
			return e
		}
		heads, e := bound.Store.reader().AllHeads()
		if e != nil {
			return e
		}
		scenes := []string{}
		seen := map[string]bool{}
		for _, head := range heads {
			scene := str(get(head, "sceneId"))
			if !seen[scene] {
				scenes = append(scenes, scene)
				seen[scene] = true
			}
		}
		return tx.Store.Read(ctx, func(reader *assertions.Reader) error {
			authority, e := assertionauthority.New(reader, options)
			if e != nil {
				return e
			}
			for _, scene := range scenes {
				c := Context{SceneID: scene}
				if e = bound.PruneUnavailableProof(c, func(evidence, proof *Object) (bool, error) {
					record, e := reader.Record(str(get(get(evidence, "backing"), "id")))
					if e != nil || record == nil {
						return false, e
					}
					if get(get(record, "scope"), "kind") != "scene" || get(get(record, "scope"), "sceneId") != scene {
						return false, nil
					}
					supported, e := authority.Supported(record)
					if e != nil || !supported {
						return false, e
					}
					for _, source := range arr(get(proof, "sources")) {
						recalled, e := reader.Recalled(str(get(source, "sceneId")), str(get(source, "messageId")))
						if e != nil || recalled {
							return false, e
						}
					}
					return authority.ProofParentsAvailable(object(get(record, "scope")), proof, "history")
				}); e != nil {
					return e
				}
				transitions := []Transition{}
				for _, from := range before {
					state, e := reader.State(from.ID)
					if e != nil {
						return e
					}
					to := Reference{ID: from.ID, Version: float64(state.Version), DeletionEpoch: float64(state.DeletionEpoch)}
					if !state.Deleted && !deepEqual(referenceValue(from), referenceValue(to)) {
						transitions = append(transitions, Transition{from, to})
					}
				}
				surviving := func(c Context, evidence *Object) (*Candidate, error) {
					id := str(get(get(evidence, "backing"), "id"))
					var captured *Reference
					for i := range before {
						if before[i].ID == id {
							captured = &before[i]
						}
					}
					if captured == nil || !deepEqual(referenceValue(*captured), boundRef(evidence)) {
						return nil, nil
					}
					state, e := reader.State(id)
					if e != nil {
						return nil, e
					}
					if state.Deleted || state.Version < 1 || (float64(state.Version) == captured.Version && float64(state.DeletionEpoch) == captured.DeletionEpoch) {
						return nil, nil
					}
					ref := Reference{ID: id, Version: float64(state.Version), DeletionEpoch: float64(state.DeletionEpoch)}
					view, e := authority.RelationAuthority(assertionauthority.Context{SceneID: c.SceneID, Simulation: c.Simulation}, ref)
					if e != nil || view == nil {
						return nil, e
					}
					return &Candidate{ref, view}, nil
				}
				if e = bound.Requalify(c, transitions, surviving); e != nil {
					return e
				}
			}
			return nil
		})
	})
}

// Avoid shadowing the service named bound in Refresh.
func boundRef(e *Object) any { return bound(e) }
