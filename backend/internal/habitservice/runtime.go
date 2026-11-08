package habitservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// RuntimeRecallPreparation is the concrete live source runtime composition.
// Compose it with other domains' preparations rather than replacing them. Its
// callback runs under the root recall writer, after tombstone/event revocation;
// a habit failure rolls back that root main transaction while intent survives.
// Preserve the source Store.recall call even though the root already recalled:
// identical payloads deduplicate in authority, but each call ACKs and attempts
// its tombstone INSERT, with observable failure ordering.
func (s *Service) RuntimeRecallPreparation() sourceevents.RecallPreparation {
	return func(ctx context.Context, session *recovery.WriteSession, _ botruntime.ReadAuthority, scene botruntime.Scene, id string) (func() error, error) {
		bound := s.WithSession(session)
		if bound.bindingError != nil {
			return nil, bound.bindingError
		}
		return func() error { _, e := bound.Recall(ctx, scene.ID, id); return e }, nil
	}
}
