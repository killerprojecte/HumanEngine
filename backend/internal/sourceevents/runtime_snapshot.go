package sourceevents

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// CurrentRuntimeSnapshot is the exact currentSourceSnapshot predicate. Binding
// the store with WithSession keeps proof and its dependent domain write on the
// same guarded connection. Foreign, expired or lost sessions fail, not false.
func (s *Store) CurrentRuntimeSnapshot(ctx context.Context, message *jsjson.Object) (current bool, err error) {
	if message == nil {
		return false, ErrInvalid
	}
	owned := cloneRuntimeJSON(message).(*jsjson.Object)
	err = s.Read(ctx, func(v *View) error {
		if e := v.ready(); e != nil {
			return e
		}
		var e error
		current, e = currentRuntimeSource(v, owned)
		if e != nil {
			return e
		}
		return v.ready()
	})
	return current && err == nil, err
}
