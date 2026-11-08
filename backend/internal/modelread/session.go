package modelread

import (
	"context"
	"database/sql"
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

// SceneID exposes only the authenticated ticket's captured scene identity.
func (t *Ticket) SceneID() string {
	if t == nil || t.self != t {
		return ""
	}
	return t.scene
}

// CheckBinding prevents composing a model-read owner from another application.
func (p *Policy) CheckBinding(authority *recovery.Authority, main *sql.DB) error {
	return p.store.CheckReadBinding(authority, main, nil)
}
