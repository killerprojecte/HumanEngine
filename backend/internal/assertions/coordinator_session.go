package assertions

import "github.com/killerprojecte/HumanEngine/backend/internal/recovery"

// WithSession joins an explicitly authorized existing writer. Transaction still
// creates an owned child savepoint and preserves its business hooks. The
// returned coordinator expires with session and rejects a different main or
// authority through Store.WithSession. AfterCommit is suppressed: the caller must
// perform post-commit work only after successful enclosing root completion.
func (c *Coordinator) WithSession(session *recovery.WriteSession) *Coordinator {
	copy := *c
	copy.store = c.store.WithSession(session)
	copy.options.AfterCommit = nil
	return &copy
}
