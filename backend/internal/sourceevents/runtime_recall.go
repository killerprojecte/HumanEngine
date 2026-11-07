package sourceevents

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// Recall reserves main once, but never retains an authority lock while running
// callbacks. The facade exposes only fixed reads, not SQL or mutation handles.
func (a *RuntimeAuthority) Recall(ctx context.Context, scene botruntime.Scene, id string, prepare func(botruntime.ReadAuthority) (func() error, error), apply func() error) error {
	if scene.Kind == "simulation" {
		return errors.New("runtime_real_recall_required")
	}
	return a.store.authority.WithWriteSession(ctx, a.store.main, func(session *recovery.WriteSession) error {
		store := a.store.WithSession(session)
		var lease atomic.Bool
		reads := &runtimeReads{store: store, lease: &lease}
		defer lease.Store(false)
		var prepared func() error
		var preparationError error
		if prepare != nil {
			func() { lease.Store(true); defer lease.Store(false); prepared, preparationError = prepare(reads) }()
		}
		// Even a failed pre-state capture must leave durable root intent ahead of
		// the main rollback. This is not ACK evidence of completed business work.
		if _, err := session.AppendIntent(recovery.Source{SceneID: scene.ID, MessageID: id}); err != nil {
			return err
		}
		if _, err := session.Exec("INSERT OR IGNORE INTO recall_tombstones VALUES(?,?,?)", text(scene.ID), text(id), store.now()); err != nil {
			return err
		}
		if preparationError != nil {
			return preparationError
		}
		var enrolled bool
		if err := store.Read(ctx, func(v *View) error { m, e := v.Registered(scene.ID, id); enrolled = m != nil; return e }); err != nil {
			return err
		}
		if enrolled {
			if err := store.RevokeAll(ctx, scene.ID, id); err != nil {
				return err
			}
		}
		if apply != nil {
			if err := apply(); err != nil {
				return err
			}
		}
		if prepared != nil {
			return func() error { lease.Store(true); defer lease.Store(false); return prepared() }()
		}
		return nil
	})
}
