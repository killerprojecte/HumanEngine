package sourceevents

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// AppendRecallPreparation composes another synchronous owner without dropping
// the already installed domain's preparation or completion hooks.
func (a *RuntimeAuthority) AppendRecallPreparation(next RecallPreparation) *RuntimeAuthority {
	if next == nil {
		copy := *a
		return &copy
	}
	previous := a.recallPreparation
	return a.WithRecallPreparation(func(ctx context.Context, ws *recovery.WriteSession, reads botruntime.ReadAuthority, scene botruntime.Scene, id string) (func() error, error) {
		var first func() error
		var e error
		if previous != nil {
			first, e = previous(ctx, ws, reads, scene, id)
			if e != nil {
				return first, e
			}
		}
		second, e := next(ctx, ws, reads, scene, id)
		if e != nil {
			return first, e
		}
		return func() error {
			if first != nil {
				if e := first(); e != nil {
					return e
				}
			}
			if second != nil {
				return second()
			}
			return nil
		}, nil
	})
}
