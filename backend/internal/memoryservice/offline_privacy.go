package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionlifecycle"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func (s *offlineFacade) terminalPrivacy(tx *assertions.RevisionTxn, id string) error {
	ctx := s.ctx
	if e := s.d.Knowledge.PurgeInMaintenanceAssertionScope(tx.Store, id); e != nil {
		return e
	}
	return tx.Store.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error {
		if e := s.d.Semantic.WithMaintenanceTx(ctx, ws).PurgeAssertion(id); e != nil {
			return e
		}
		_, e := s.mutations(ws).TerminalAssertionDeleted(id)
		return e
	})
}
func (s *offlineFacade) deletePrivacy(ctx context.Context, tx *assertions.RevisionTxn, id string) (bool, error) {
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
	if e = tx.Store.WithMaintenanceScope(func(ws *recovery.MaintenanceTx) error { return privacy.WithMaintenanceTx(ctx, ws).PurgeAssertion(id) }); e != nil {
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
func (s *offlineFacade) refreshAllPrivacy(ctx context.Context, tx *assertions.RevisionTxn, p *commitments.PrivacyService, before []commitments.Reference, captured map[string][]*Object) error {
	if e := p.Refresh(ctx, tx, before, s.d.Authority); e != nil {
		return e
	}
	if e := s.refreshKnowledgePrivacy(ctx, tx, before); e != nil {
		return e
	}
	return s.refreshSemanticPrivacy(ctx, tx, before, captured)
}
