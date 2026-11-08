package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

func (s *offlineFacade) privacyOwner(ctx context.Context, tx *assertions.RevisionTxn) (out *commitments.PrivacyService, err error) {
	err = tx.Store.WithMaintenanceScope(func(scope *recovery.MaintenanceTx) error {
		var e error
		out, e = commitments.NewPrivacyService(s.d.Commitments.WithMaintenanceTx(ctx, scope), commitments.Owners{AfterWrite: s.planCommitted})
		return e
	})
	return
}
func (s *offlineFacade) planCommitted(store *commitments.Store, head *Object, next, prior []any) error {
	ctx := s.ctx
	return store.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
		workspace := s.d.WorkingSet.WithMaintenanceTx(ctx, ws)
		scene := text(head, "sceneId")
		entities := []workingset.Entity{{Kind: "commitment", ID: text(head, "id")}}
		if value(head, "deleted") == true {
			if e := workspace.TerminalInvalidate(ctx, scene, entities); e != nil {
				return e
			}
		} else {
			if e := workspace.Invalidate(ctx, scene, entities); e != nil {
				return e
			}
		}
		hooks := s.mutations(ws)
		c := workingset.Context{SceneID: scene}
		if value(head, "deleted") != true {
			first := value(head, "activeVersion")
			if first == nil {
				first = value(head, "latestVersion")
			}
			versions := []any{first}
			if first != value(head, "latestVersion") {
				versions = append(versions, value(head, "latestVersion"))
			}
			mv, _ := value(head, "mutationVersion").(float64)
			epoch, _ := value(head, "deletionEpoch").(float64)
			for _, v := range versions {
				version, _ := v.(float64)
				if _, e := hooks.Committed(c, []workingset.Reference{{Kind: "commitment", ID: text(head, "id"), Version: version, DeletionEpoch: epoch, MutationVersion: &mv}}); e != nil {
					return e
				}
			}
		}
		refs := []workingset.Reference{}
		indices := map[string]int{}
		for _, revisions := range [][]any{prior, next} {
			for _, rev := range revisions {
				ev := append([]any{}, epArr(epGet(rev, "evidence"))...)
				for _, group := range epArr(epGet(rev, "requiredProposalGroups")) {
					ev = append(ev, epArr(group)...)
				}
				for _, v := range ev {
					ref := epGet(v, "qualificationRef")
					if ref == nil {
						ref = epGet(v, "backing")
					}
					r := semanticRef(object(ref))
					wr := workingset.Reference{Kind: "assertion", ID: r.ID, Version: r.Version, DeletionEpoch: r.DeletionEpoch}
					if i, ok := indices[r.ID]; ok {
						refs[i] = wr
					} else {
						indices[r.ID] = len(refs)
						refs = append(refs, wr)
					}
				}
			}
		}
		for i := 0; i < len(refs); i += 40 {
			end := i + 40
			if end > len(refs) {
				end = len(refs)
			}
			if _, e := hooks.SourceEdited(c, refs[i:end]); e != nil {
				return e
			}
		}
		// No dirty listener is attached by source offline recovery.
		return nil
	})
}
func (s *offlineFacade) capturePrivacyRefs(ctx context.Context, st *assertions.Store) (out []commitments.Reference, err error) {
	err = st.Read(ctx, func(r *assertions.Reader) error {
		var e error
		out, e = commitments.ReadPrivacyRefs(r, s.d.Authority)
		return e
	})
	return
}
func (s *offlineFacade) proofAvailable(r *assertions.Reader, a *assertionauthority.Authority, scene string, ref, proof *Object) (bool, error) {
	record, e := r.Record(text(ref, "id"))
	if e != nil || record == nil {
		return false, e
	}
	scope := object(value(record, "scope"))
	if text(scope, "kind") != "scene" || text(scope, "sceneId") != scene {
		return false, nil
	}
	ok, e := a.Supported(record)
	if e != nil || !ok {
		return false, e
	}
	for _, source := range epArr(value(proof, "sources")) {
		v := object(source)
		recalled, e := r.Recalled(text(v, "sceneId"), text(v, "messageId"))
		if e != nil || recalled {
			return false, e
		}
	}
	return a.ProofParentsAvailable(scope, proof, "history")
}
func (s *offlineFacade) refreshKnowledgePrivacy(ctx context.Context, tx *assertions.RevisionTxn, before []commitments.Reference) error {
	if e := tx.Checkpoint(); e != nil {
		return e
	}
	return tx.Store.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
		return tx.Store.Read(ctx, func(r *assertions.Reader) error {
			changes, _, e := privacyChanges(r, before)
			if e != nil {
				return e
			}
			a, e := assertionauthority.New(r, s.d.Authority)
			if e != nil {
				return e
			}
			store := s.d.Knowledge.WithMaintenanceTx(ctx, ws).Storage
			rows, e := store.Rows(nil, nil)
			if e != nil {
				return e
			}
			seen := map[string]bool{}
			l := knowledge.NewLifecycle(store)
			for _, row := range rows {
				scene := text(row, "sceneId")
				if seen[scene] {
					continue
				}
				seen[scene] = true
				if e = l.PruneUnavailableProofChecked(scene, false, func(ref, proof *Object) (bool, error) { return s.proofAvailable(r, a, scene, ref, proof) }); e != nil {
					return e
				}
				if e = l.Requalify(knowledge.Context{SceneID: scene, Provenance: "source-only"}, changes, func(c knowledge.Context, ref knowledge.Reference) (*knowledge.Assertion, error) {
					return a.RelationAuthority(assertionauthority.Context{SceneID: c.SceneID}, ref)
				}); e != nil {
					return e
				}
			}
			return nil
		})
	})
}
func (s *offlineFacade) refreshSemanticPrivacy(ctx context.Context, tx *assertions.RevisionTxn, before []commitments.Reference, captured map[string][]*Object) error {
	if e := tx.Checkpoint(); e != nil {
		return e
	}
	return tx.Store.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
		return tx.Store.Read(ctx, func(r *assertions.Reader) error {
			_, changes, e := privacyChanges(r, before)
			if e != nil {
				return e
			}
			a, e := assertionauthority.New(r, s.d.Authority)
			if e != nil {
				return e
			}
			store := s.d.Semantic.WithMaintenanceTx(ctx, ws)
			rows, e := store.Rows(nil)
			if e != nil {
				return e
			}
			seen := map[string]bool{}
			for _, row := range rows {
				scene := text(row, "sceneId")
				if seen[scene] {
					continue
				}
				seen[scene] = true
				c := semantic.Context{SceneID: scene}
				if e = store.PruneUnavailableProof(c, func(ref, proof *Object) (bool, error) { return s.proofAvailable(r, a, scene, ref, proof) }); e != nil {
					return e
				}
				if e = store.Requalify(c, changes, captured[scene], s.semanticPrivacyAuthority(ctx, tx, ws)); e != nil {
					return e
				}
			}
			return nil
		})
	})
}
