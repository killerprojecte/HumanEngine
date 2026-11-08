package sourceevents

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

func NewMaintenanceStore(owner *recovery.Maintenance, tx *recovery.MaintenanceTx, ctx context.Context) (*Store, error) {
	if owner == nil || ctx == nil {
		return nil, errors.New("recovery_maintenance_required")
	}
	s := &Store{maintenance: owner, maintenanceTx: tx, maintenanceContext: ctx, revisions: 32, edits: 128, listeners: &listeners{members: map[*Listener]*listenerNode{}}}
	if e := s.ready(); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *Store) recordOffline(m Mutation) error {
	if e := s.ready(); e != nil {
		return e
	}
	if e := s.maintenanceTx.RequireReader(s.maintenanceContext, 2); e != nil {
		return e
	}
	switch m.Kind {
	case "recalled":
		return s.maintenanceTx.RecordReplayEffect(s.maintenanceContext, recovery.Source{SceneID: m.Identity.SceneID, MessageID: m.Identity.MessageID})
	case "edited":
		if m.Metadata == nil || m.Metadata.Ref != m.Ref || m.Metadata.Identity != m.Identity || m.HeadHash == "" {
			return errors.New("recovery_source_event_invalid")
		}
		key, e := IdentityKey(m.Identity)
		if e != nil {
			return e
		}
		return s.maintenanceTx.RecordReplayEffect(s.maintenanceContext, recovery.SourceEditHead{SceneID: m.Identity.SceneID, MessageID: m.Identity.MessageID, EventID: m.Ref.EventID, Revision: m.Ref.Revision, DeletionEpoch: m.Ref.DeletionEpoch, IdentityHash: key, HeadHash: m.HeadHash})
	}
	return nil
}
