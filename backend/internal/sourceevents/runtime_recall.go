package sourceevents

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// RecallPreparation captures privacy state using the exact root writer. The
// returned callback executes after root recall intent, tombstone and revocation.
// Both phases are synchronous. Retained sessions expire at root return; never
// open a separate writer or reenter the unbound authority from either phase.
type RecallPreparation func(context.Context, *recovery.WriteSession, botruntime.ReadAuthority, botruntime.Scene, string) (func() error, error)

// WithRecallPreparation returns a configured copy; it does not mutate an
// authority already used by a runtime. A nil preparation removes the binding.
func (a *RuntimeAuthority) WithRecallPreparation(prepare RecallPreparation) *RuntimeAuthority {
	copy := *a
	copy.recallPreparation = prepare
	return &copy
}

// WithRecallCompletion returns an immutable copy with post-commit work. The hook
// runs exactly once after a successful root commit and after the root writer has
// been released, so it may acquire its own projection writer. A hook error is
// returned without undoing the committed recall or replaying its business work.
// A nil hook removes the binding. It is never called on a failed root operation.
func (a *RuntimeAuthority) WithRecallCompletion(complete func(context.Context) error) *RuntimeAuthority {
	copy := *a
	copy.recallCompletion = complete
	return &copy
}

// Recall retains the runtime's read-only callback contract. Trusted memory
// composition must be explicitly installed through WithRecallPreparation.
func (a *RuntimeAuthority) Recall(ctx context.Context, scene botruntime.Scene, id string, prepare func(botruntime.ReadAuthority) (func() error, error), apply func() error) error {
	var bound func(*recovery.WriteSession, botruntime.ReadAuthority) (func() error, error)
	if prepare != nil {
		bound = func(_ *recovery.WriteSession, reads botruntime.ReadAuthority) (func() error, error) {
			return prepare(reads)
		}
	}
	return a.RecallWithSession(ctx, scene, id, bound, apply)
}

// RecallWithSession additionally admits a trusted same-root preparation. The
// configured preparation runs first, and its business callback runs before the
// caller's prepared callback. Callback failure rolls back main, not root intent.
func (a *RuntimeAuthority) RecallWithSession(ctx context.Context, scene botruntime.Scene, id string, prepare func(*recovery.WriteSession, botruntime.ReadAuthority) (func() error, error), apply func() error) error {
	if scene.Kind == "simulation" {
		return errors.New("runtime_real_recall_required")
	}
	err := a.store.authority.WithWriteSession(ctx, a.store.main, func(session *recovery.WriteSession) error {
		store := a.store.WithSession(session)
		var lease atomic.Bool
		reads := &runtimeReads{store: store, lease: &lease}
		defer lease.Store(false)
		var prepared []func() error
		var preparationError error
		func() {
			lease.Store(true)
			defer lease.Store(false)
			if a.recallPreparation != nil {
				var callback func() error
				callback, preparationError = a.recallPreparation(ctx, session, reads, scene, id)
				if preparationError != nil {
					return
				}
				if callback != nil {
					prepared = append(prepared, callback)
				}
			}
			if prepare != nil {
				var callback func() error
				callback, preparationError = prepare(session, reads)
				if callback != nil {
					prepared = append(prepared, callback)
				}
			}
		}()
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
		lease.Store(true)
		defer lease.Store(false)
		for _, callback := range prepared {
			if err := session.CheckBinding(a.store.authority, a.store.main); err != nil {
				return err
			}
			if err := callback(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if a.recallCompletion != nil {
		return a.recallCompletion(ctx)
	}
	return nil
}
