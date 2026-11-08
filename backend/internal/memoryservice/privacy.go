package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/identityprivacy"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"sync/atomic"
)

func (s *Service) terminalPrivacy(tx *assertions.RevisionTxn, id string) error {
	if e := s.d.Knowledge.PurgeInAssertionScope(tx.Store, id); e != nil {
		return e
	}
	return tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
		if e := s.d.Semantic.WithSession(ws).PurgeAssertion(id); e != nil {
			return e
		}
		_, e := s.mutations(ws).TerminalAssertionDeleted(id)
		return e
	})
}
func (s *Service) Delete(ctx context.Context, id string) (deleted bool, err error) {
	err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		var e error
		deleted, e = s.deletePrivacy(ctx, tx, id)
		return e
	})
	if err != nil {
		return false, err
	}
	return
}
func (s *Service) deletePrivacy(ctx context.Context, tx *assertions.RevisionTxn, id string) (bool, error) {
	var memory *Object
	if e := tx.Store.Read(ctx, func(r *assertions.Reader) error { var e error; memory, e = r.Record(id); return e }); e != nil {
		return false, e
	}
	if memory == nil {
		return false, nil
	}
	before, e := s.capturePrivacyRefs(ctx, tx.Store)
	if e != nil {
		return false, e
	}
	captured, e := s.captureSemanticPrivacy(ctx, tx)
	if e != nil {
		if intentErr := tx.Store.RevokeTerminalAssertion(id); intentErr != nil {
			return false, intentErr
		}
		return false, e
	}
	if e = tx.Store.RevokeTerminalAssertion(id); e != nil {
		return false, e
	}
	if e = tx.Store.DeletePrivacyFingerprints(memory); e != nil {
		return false, e
	}
	lifecycle, e := assertionlifecycle.New(tx, s.owners(), s.d.Authority)
	if e != nil {
		return false, e
	}
	if e = lifecycle.PurgeHistoryDependents(id, nil, nil); e != nil {
		return false, e
	}
	if e = s.purgeRelations(tx.Store, id); e != nil {
		return false, e
	}
	privacy, e := s.privacyOwner(ctx, tx)
	if e != nil {
		return false, e
	}
	if e = tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error { return privacy.WithSession(ws).PurgeAssertion(id) }); e != nil {
		return false, e
	}
	if e = s.terminalPrivacy(tx, id); e != nil {
		return false, e
	}
	if _, e = tx.Store.Erase(id); e != nil {
		return false, e
	}
	if e = lifecycle.InvalidateDependents(id, nil); e != nil {
		return false, e
	}
	if e = tx.Store.RemovePrivacyRecord(id); e != nil {
		return false, e
	}
	if e = tx.Transaction(ctx, s.refresh); e != nil {
		return false, e
	}
	if e = s.refreshAllPrivacy(ctx, tx, privacy, before, captured); e != nil {
		return false, e
	}
	return true, nil
}
func (s *Service) refreshAllPrivacy(ctx context.Context, tx *assertions.RevisionTxn, p *commitments.PrivacyService, before []commitments.Reference, captured map[string][]*Object) error {
	if e := p.Refresh(ctx, tx, before, s.d.Authority); e != nil {
		return e
	}
	if e := s.refreshKnowledgePrivacy(ctx, tx, before); e != nil {
		return e
	}
	return s.refreshSemanticPrivacy(ctx, tx, before, captured)
}
func (s *Service) Recall(ctx context.Context, scene, message string) (affected []string, err error) {
	err = s.revisions.Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		before, e := s.capturePrivacyRefs(ctx, tx.Store)
		if e != nil {
			if intentErr := tx.Store.RecallSource(scene, message); intentErr != nil {
				return intentErr
			}
			return e
		}
		captured, e := s.captureSemanticPrivacy(ctx, tx)
		if e != nil {
			if intentErr := tx.Store.RecallSource(scene, message); intentErr != nil {
				return intentErr
			}
			return e
		}
		return tx.Transaction(ctx, func(child *assertions.RevisionTxn) error {
			var e error
			affected, e = s.recallPrepared(ctx, child, scene, message, before, captured, false)
			return e
		})
	})
	if err != nil {
		return nil, err
	}
	return
}

// RuntimeRecallPreparation is installed on the actual runtime authority. Capture
// and execution borrow one root owner; only this callback consumes the snapshot.
// No caller-supplied snapshot or second independent writer is accepted.
func (s *Service) RuntimeRecallPreparation() sourceevents.RecallPreparation {
	return func(ctx context.Context, ws *recovery.WriteSession, _ botruntime.ReadAuthority, scene botruntime.Scene, message string) (func() error, error) {
		var before []commitments.Reference
		var captured map[string][]*Object
		e := s.revisions.WithSession(ws).Transaction(ctx, func(tx *assertions.RevisionTxn) error {
			var e error
			before, e = s.capturePrivacyRefs(ctx, tx.Store)
			if e != nil {
				return e
			}
			captured, e = s.captureSemanticPrivacy(ctx, tx)
			return e
		})
		if e != nil {
			return nil, e
		}
		var used atomic.Bool
		return func() error {
			if !used.CompareAndSwap(false, true) {
				return errors.New("memory_recall_preparation_consumed")
			}
			return s.revisions.WithSession(ws).Transaction(ctx, func(tx *assertions.RevisionTxn) error {
				_, e := s.recallPrepared(ctx, tx, scene.ID, message, before, captured, true)
				return e
			})
		}, nil
	}
}
func (s *Service) recallPrepared(ctx context.Context, tx *assertions.RevisionTxn, scene, message string, before []commitments.Reference, captured map[string][]*Object, rootApplied bool) ([]string, error) {
	if !rootApplied {
		if e := tx.Store.RecallSource(scene, message); e != nil {
			return nil, e
		}
	}
	if e := tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error {
		if e := identityprivacy.RecallSource(ws, scene, message); e != nil {
			return e
		}
		if e := knowledge.NewLifecycle(s.d.Knowledge.WithSession(ws).Storage).PurgeSource(scene, message); e != nil {
			return e
		}
		return s.d.Semantic.WithSession(ws).PurgeSource(scene, message)
	}); e != nil {
		return nil, e
	}
	privacy, e := s.privacyOwner(ctx, tx)
	if e != nil {
		return nil, e
	}
	if e = tx.Store.WithWriteSession(func(ws *recovery.WriteSession) error { return privacy.WithSession(ws).PurgeSource(scene, message) }); e != nil {
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

// BindRecallAuthority installs both lifecycle work and post-root projection
// flushing. The original runtime authority remains unchanged.
func (s *Service) BindRecallAuthority(a *sourceevents.RuntimeAuthority) *sourceevents.RuntimeAuthority {
	return a.WithRecallPreparation(s.RuntimeRecallPreparation()).WithRecallCompletion(func(ctx context.Context) error {
		if _, e := s.d.Projections.Flush(ctx); e != nil {
			s.d.Log("warn", "memory", "memory_projection_refresh_failed")
		}
		return nil
	})
}

// PrepareRecall captures in an explicit caller-owned root and is consumed once.
// The owner must remain active until execution; expired capabilities fail closed.
func (s *Service) PrepareRecall(ctx context.Context, ws *recovery.WriteSession, scene, message string) (func() ([]string, error), error) {
	var before []commitments.Reference
	var captured map[string][]*Object
	e := s.revisions.WithSession(ws).Transaction(ctx, func(tx *assertions.RevisionTxn) error {
		var e error
		before, e = s.capturePrivacyRefs(ctx, tx.Store)
		if e != nil {
			return e
		}
		captured, e = s.captureSemanticPrivacy(ctx, tx)
		return e
	})
	if e != nil {
		return nil, e
	}
	var used atomic.Bool
	return func() (out []string, err error) {
		if !used.CompareAndSwap(false, true) {
			return nil, errors.New("memory_recall_preparation_consumed")
		}
		err = s.revisions.WithSession(ws).Transaction(ctx, func(tx *assertions.RevisionTxn) error {
			var e error
			out, e = s.recallPrepared(ctx, tx, scene, message, before, captured, false)
			return e
		})
		if err != nil {
			return nil, err
		}
		return
	}, nil
}
