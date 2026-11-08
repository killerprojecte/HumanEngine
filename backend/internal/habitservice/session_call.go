package habitservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// TransactionWithSession joins synchronous source proof and domain writes under
// one owned writer. The child session expires at callback return; never retain
// it, open another authority scope, or use the suspended parent within fn.
func (s *Service) TransactionWithSession(ctx context.Context, fn func(*Service, *recovery.WriteSession) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if fn == nil {
		return errors.New("habit_transaction_required")
	}
	call := func(ws *recovery.WriteSession) error {
		if e := ws.CheckBinding(s.authority, s.main); e != nil {
			return e
		}
		if e := ws.AssertReady(); e != nil {
			return e
		}
		return fn(s.WithSession(ws), ws)
	}
	if s.session != nil {
		return s.session.WithSavepoint(call)
	}
	return s.authority.WithWriteSession(ctx, s.main, call)
}
