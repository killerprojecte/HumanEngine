package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/identityprivacy"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func (o *OfflineLifecycle) Recall(ctx context.Context, scene, message string) ([]string, error) {
	return o.within(ctx, func(s *offlineFacade, tx *assertions.RevisionTxn) error {
		before, e := s.capturePrivacyRefs(ctx, tx.Store)
		if e != nil {
			return e
		}
		captured, e := s.captureSemanticPrivacy(ctx, tx)
		if e != nil {
			return e
		}
		return tx.Transaction(ctx, func(child *assertions.RevisionTxn) error {
			_, e := s.recallPrepared(ctx, child, scene, message, before, captured, false)
			return e
		})
	})
}
func (s *offlineFacade) recallPrepared(ctx context.Context, tx *assertions.RevisionTxn, scene, message string, before []commitments.Reference, captured map[string][]*Object, rootApplied bool) ([]string, error) {
	if !rootApplied {
		if e := tx.Store.RecallSource(scene, message); e != nil {
			return nil, e
		}
	}
	if e := tx.Store.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
		if e := identityprivacy.AssertOfflineSourceClean(ctx, s.owner, ws, scene, message); e != nil {
			return e
		}
		if e := knowledge.NewLifecycle(s.d.Knowledge.WithMaintenanceTx(ctx, ws).Storage).PurgeSource(scene, message); e != nil {
			return e
		}
		return s.d.Semantic.WithMaintenanceTx(ctx, ws).PurgeSource(scene, message)
	}); e != nil {
		return nil, e
	}
	privacy, e := s.privacyOwner(ctx, tx)
	if e != nil {
		return nil, e
	}
	if e = tx.Store.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
		return privacy.WithMaintenanceTx(ctx, ws).PurgeSource(scene, message)
	}); e != nil {
		return nil, e
	}
	redacted, e := tx.Store.RedactSource(scene, message)
	if e != nil {
		return nil, e
	}
	lifecycle, e := assertionlifecycle.New(tx, s.owners(), s.d.Authority)
	if e != nil {
		return nil, e
	}
	purged, already := assertionlifecycle.IDSet{}, assertionlifecycle.IDSet{}
	for _, id := range redacted {
		already[id] = struct{}{}
	}
	for _, id := range redacted {
		if e = s.purgeRelations(tx.Store, id); e != nil {
			return nil, e
		}
		if e = lifecycle.PurgeHistoryDependents(id, purged, already); e != nil {
			return nil, e
		}
	}
	var records []*Object
	if e = tx.Store.Read(ctx, func(r *assertions.Reader) error { var e error; records, e = r.SourceRecords(scene, message); return e }); e != nil {
		return nil, e
	}
	affected := []string{}
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			affected = append(affected, id)
		}
	}
	for _, previous := range records {
		id := text(previous, "id")
		if e = lifecycle.PurgeHistoryDependents(id, purged, already); e != nil {
			return nil, e
		}
		sources := []any{}
		for _, source := range epArr(value(previous, "sources")) {
			if epGet(source, "sceneId") != scene || epGet(source, "messageId") != message {
				sources = append(sources, source)
			}
		}
		next := previous.Clone()
		_ = next.Set("sources", sources)
		confidence, _ := value(previous, "confidence").(float64)
		_ = next.Set("confidence", confidence*float64(len(sources))/float64(len(epArr(value(previous, "sources")))))
		status := text(previous, "status")
		if status != "superseded" && status != "invalidated" {
			status = "disputed"
			if len(sources) == 0 {
				status = "invalidated"
			}
		}
		_ = next.Set("status", status)
		var proofs []any
		if e = tx.Store.Read(ctx, func(r *assertions.Reader) error { var e error; proofs, e = r.Proofs(id); return e }); e != nil {
			return nil, e
		}
		for i, p := range proofs {
			match := false
			for _, source := range epArr(epGet(p, "sources")) {
				match = match || epGet(source, "sceneId") == scene && epGet(source, "messageId") == message
			}
			if match {
				copy := object(p).Clone()
				copy.Delete("subjectWitness")
				copy.Delete("sourceObservations")
				proofs[i] = copy
			}
		}
		if e = tx.Repository.SaveSupports(id, proofs); e != nil {
			return nil, e
		}
		if e = tx.Repository.Write(next); e != nil {
			return nil, e
		}
		if len(sources) == 0 {
			if e = tx.Store.RemoveVector(id); e != nil {
				return nil, e
			}
		}
		add(id)
	}
	var all []any
	if e = tx.Store.Read(ctx, func(r *assertions.Reader) error { var e error; all, e = r.All(); return e }); e != nil {
		return nil, e
	}
	for _, raw := range all {
		memory := object(raw)
		if text(memory, "lineageStatus") != "unverified" {
			continue
		}
		scope := object(value(memory, "scope"))
		matches := text(scope, "kind") == "scene" && text(scope, "sceneId") == scene
		for _, source := range epArr(value(memory, "sources")) {
			matches = matches || epGet(source, "sceneId") == scene
		}
		if !matches {
			continue
		}
		id := text(memory, "id")
		if e = lifecycle.PurgeHistoryDependents(id, purged, already); e != nil {
			return nil, e
		}
		next := memory.Clone()
		_ = next.Set("lineageStatus", "quarantined")
		if e = tx.Repository.Write(next); e != nil {
			return nil, e
		}
		if e = tx.Store.RemoveVector(id); e != nil {
			return nil, e
		}
		add(id)
	}
	if e = tx.Transaction(ctx, func(child *assertions.RevisionTxn) error {
		l, e := assertionlifecycle.New(child, s.owners(), s.d.Authority)
		if e != nil {
			return e
		}
		ids, e := l.RefreshSupport()
		if e != nil {
			return e
		}
		for _, id := range ids {
			add(id)
		}
		return nil
	}); e != nil {
		return nil, e
	}
	if e = s.refreshAllPrivacy(ctx, tx, privacy, before, captured); e != nil {
		return nil, e
	}
	return affected, nil
}
