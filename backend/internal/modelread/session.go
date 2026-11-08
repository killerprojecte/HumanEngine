package modelread

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// ValidInSession validates the original policy-owned ticket using the caller's
// exact active writer. It never opens another root read during final commit.
func (p *Policy) ValidInSession(ctx context.Context, t *Ticket, session *recovery.WriteSession) bool {
	if p == nil || t == nil || t.self != t || t.owner != p || session == nil {
		return false
	}
	var valid bool
	err := p.store.WithSession(session).Read(ctx, func(r *assertions.Reader) error {
		row, e := r.Scene(t.scene)
		if e != nil {
			return e
		}
		enabled, revision, personal, e := sceneState(row)
		valid = e == nil && enabled && revision == t.revision && personal == t.personalization
		return e
	})
	return err == nil && valid
}
