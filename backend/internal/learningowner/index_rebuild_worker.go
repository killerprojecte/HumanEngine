package learningowner

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// The installation supplies the builder for its pinned operation router. It
// must reuse the installation scheduler, physical host, ledger and credentials.
type indexRouterBuilder func(context.Context, *Object) (*providertransport.NativeModels, error)

type indexWork struct {
	mu      sync.Mutex
	task    *indexTask
	closing bool
}
type indexTask struct {
	lease     indexRebuildLease
	cancel    context.CancelFunc
	done      chan struct{}
	started   chan struct{}
	startOnce sync.Once
	err       error
	child     *providertransport.NativeModels
}

func (o *Owner) indexWriter(ctx context.Context, fn func(*recovery.WriteSession) error) error {
	return o.d.Authority.WithWriteSession(ctx, o.d.Main, fn)
}

// dispatchIndexWork admits a fresh or explicit resumed job. There is no caller
// during application startup and no HTTP binding in this slice.
func (o *Owner) dispatchIndexWork(ctx context.Context, expected *string, revision string, resume, confirmed bool, builder indexRouterBuilder) (*Object, error) {
	w := &o.rebuild
	w.mu.Lock()
	defer w.mu.Unlock()
	if o.closed.Load() || w.closing {
		return nil, errors.New("embedding_rebuild_closed")
	}
	if w.task != nil {
		return nil, errors.New("embedding_rebuild_busy")
	}
	if builder == nil {
		return nil, errors.New("embedding_rebuild_router_required")
	}
	if o.indexOwner == "" {
		id, err := indexID()
		if err != nil {
			return nil, err
		}
		o.indexOwner = id
	}
	var lease indexRebuildLease
	lease.owner = o.indexOwner
	var err error
	if lease.attempt, err = indexID(); err != nil {
		return nil, err
	}
	if !resume {
		if lease.job, err = indexID(); err != nil {
			return nil, err
		}
	}
	var job, config *Object
	var selected *selectedEncoder
	err = o.indexWriter(ctx, func(ws *recovery.WriteSession) error {
		state, raw, err := readIndexValues(func(q string, a ...any) (indexRows, error) { return ws.Query(q, a...) })
		if err != nil {
			return err
		}
		if err := checkExpectedIndexJob(state.job, expected); err != nil {
			return err
		}
		if raw == nil {
			return errors.New("embedding_model_not_configured")
		}
		// Match Store.models: capture metadata and credential presence together.
		// Plaintext remains with the installed transport's credential owner.
		config, err = credentials.ReadModelConfiguration(raw, func(namespace, name string) (bool, error) {
			rows, err := ws.Query("SELECT EXISTS(SELECT 1 FROM secrets WHERE namespace=? AND name=?)", namespace, name)
			if err != nil {
				return false, err
			}
			var exists bool
			if rows.Next() {
				err = rows.Scan(&exists)
			}
			return exists, errors.Join(err, rows.Err(), rows.Close())
		})
		if err != nil {
			return err
		}
		selected, err = selectEncoder(config)
		if err != nil || selected == nil {
			if err == nil {
				err = errors.New("embedding_model_not_configured")
			}
			return err
		}
		if resume {
			job, err = o.resumeIndexRebuildInSession(ws, expected, lease, confirmed)
			if err == nil {
				lease.job = str(job, "id")
			}
			return err
		}
		if selected.revision != revision {
			return errors.New("embedding_config_changed")
		}
		if err := o.expireIndexLease(ws, state.job); err != nil {
			return err
		}
		if str(state.job, "state") == "running" {
			return errors.New("embedding_rebuild_busy")
		}
		rows, err := o.d.Memory.IndexableInSession(ctx, ws)
		if err != nil {
			return err
		}
		job, err = o.submitIndexRebuildInSession(ws, expected, revision, lease, len(rows))
		if err != nil {
			return err
		}
		return pruneIndexGenerations(ws, state.active, job)
	})
	if err != nil {
		return nil, err
	}
	o.indexBootstrap.Store(false)
	owned, cancel := context.WithCancel(context.Background())
	task := &indexTask{lease: lease, cancel: cancel, done: make(chan struct{}), started: make(chan struct{})}
	w.task = task
	go func() {
		defer func() {
			cancel()
			w.mu.Lock()
			close(task.done)
			if task.err == nil {
				w.task = nil
			}
			w.mu.Unlock()
			task.signalStarted()
		}()
		task.err = o.runIndexWork(owned, task, selected, func(ctx context.Context) (*providertransport.NativeModels, error) { return builder(ctx, config) })
	}()
	return job, nil
}

func (task *indexTask) signalStarted() {
	task.startOnce.Do(func() { close(task.started) })
}

// The first paid batch signals only after its durable reservation. A job with
// nothing to dispatch signals after terminal work, never by inventing status.
func (o *Owner) awaitInitialIndexWork(ctx context.Context, job *Object) error {
	o.rebuild.mu.Lock()
	task := o.rebuild.task
	matches := task != nil && task.lease.identifies(job)
	o.rebuild.mu.Unlock()
	if !matches {
		return nil
	}
	select {
	case <-task.started:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Owner) stopIndexWork(ctx context.Context, expected *string, cancel bool) (job *Object, err error) {
	w := &o.rebuild
	w.mu.Lock()
	defer w.mu.Unlock()
	err = o.indexWriter(ctx, func(ws *recovery.WriteSession) error {
		var err error
		job, err = stopIndexRebuildInSession(ws, expected, cancel)
		return err
	})
	if err == nil && w.task != nil && w.task.lease.identifies(job) {
		w.task.cancel()
	}
	return
}

func (o *Owner) closeIndexWork(ctx context.Context) error {
	w := &o.rebuild
	w.mu.Lock()
	w.closing = true
	task := w.task
	if task == nil {
		w.mu.Unlock()
		return nil
	}
	task.cancel()
	w.mu.Unlock()
	err := o.indexWriter(ctx, func(ws *recovery.WriteSession) error {
		_, err := pauseOwnedIndexRebuildInSession(ws, task.lease)
		return err
	})
	select {
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	case <-task.done:
		if task.err != nil && task.child != nil {
			// An uncertain early drain result isn't physical termination. Retry
			// the same child through its own close path without replaying work.
			return errors.Join(err, task.child.CloseAndWait(ctx))
		}
		return errors.Join(err, task.err)
	}
}

func (o *Owner) indexBatchGuard(ctx context.Context, rows []*Object) (context.Context, func(*recovery.WriteSession) error, error) {
	tickets := map[string]*modelread.Ticket{}
	ordered := []*modelread.Ticket{}
	for _, row := range rows {
		scope := indexObject(get(row, "scope"))
		if str(scope, "kind") != "scene" {
			continue
		}
		scene := str(scope, "sceneId")
		ticket := tickets[scene]
		if ticket == nil {
			var err error
			ticket, err = o.d.Read.Require(ctx, scene)
			if err != nil {
				return ctx, nil, err
			}
			tickets[scene] = ticket
			ordered = append(ordered, ticket)
		}
		if !ticket.PersonalizationEnabled() && (str(scope, "personId") != "" || str(row, "kind") == "summary" || str(row, "kind") == "experience") {
			return ctx, nil, errors.New("personalization_disabled")
		}
	}
	guard := func(ws *recovery.WriteSession) error {
		for _, ticket := range ordered {
			if ws == nil {
				if err := o.d.Read.Assert(ctx, ticket); err != nil {
					return err
				}
			} else if !o.d.Read.ValidInSession(ctx, ticket, ws) {
				return errors.New("memory_read_revoked")
			}
		}
		return nil
	}
	return providertransport.WithModelReadGuard(ctx, func(context.Context) error { return guard(nil) }), guard, nil
}

func (o *Owner) runIndexWork(ctx context.Context, task *indexTask, selected *selectedEncoder, factory providertransport.RouterFactory) error {
	ctx = providertransport.WithBudgetContext(ctx, "embedding-rebuild:"+task.lease.job, "maintenance")
	stopBeat := make(chan struct{})
	beatDone := make(chan struct{})
	go func() {
		defer close(beatDone)
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stopBeat:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := o.indexWriter(ctx, func(ws *recovery.WriteSession) error {
					now := o.d.Now().UnixMilli()
					job, err := ownedIndexRebuildInSession(ws, task.lease, now)
					if err != nil {
						return err
					}
					if job == nil {
						return errIndexWorkStop
					}
					return renewIndexRebuildInSession(ws, job, now)
				}); err != nil {
					task.cancel()
					return
				}
			}
		}
	}()
	defer func() { close(stopBeat); <-beatDone }()
	var router *providertransport.NativeModels
	var release func()
	err := o.indexWorkLoop(ctx, task.lease, selected, task.signalStarted, func(ctx context.Context, inputs []string) (*providertransport.EmbeddingResult, error) {
		if router == nil {
			var err error
			router, release, err = o.d.Models.AcquireOperationRouter(ctx, factory)
			task.child = router
			if err != nil {
				return nil, err
			}
		}
		return router.Embeddings(ctx, selected.model, inputs)
	})
	if err != nil && !errors.Is(err, errIndexWorkStop) && ctx.Err() == nil {
		o.failIndexWork(ctx, task.lease, err)
	}
	if release != nil {
		defer release()
		// The child owns physical attempts that can outlive a cancelled
		// logical request. Do not admit a new job while that child drains.
		return router.CloseAndWait(context.Background())
	}
	return nil
}

var errIndexWorkStop = errors.New("embedding_rebuild_stopped")

func (o *Owner) failIndexWork(ctx context.Context, lease indexRebuildLease, err error) {
	e := o.indexWriter(ctx, func(ws *recovery.WriteSession) error {
		now := o.d.Now().UnixMilli()
		job, writeErr := ownedIndexRebuildInSession(ws, lease, now)
		if writeErr != nil || job == nil {
			return writeErr
		}
		code := "embedding_rebuild_failed"
		var model *modelservice.ModelError
		if errors.As(err, &model) {
			code = model.Code
			if model.Status != nil {
				_ = job.Set("requiresRetryConfirmation", false)
			}
		} else {
			switch err.Error() {
			case "memory_read_disabled", "memory_read_revoked", "personalization_disabled", "embedding_dimensions_changed", "embedding_active_generation_changed", "embedding_lease_lost", "invalid_embedding_response":
				code = err.Error()
			}
		}
		_ = job.Set("state", "failed")
		_ = job.Set("error", code)
		return renewIndexRebuildInSession(ws, job, now)
	})
	if e != nil {
		o.log("warn", "embedding_rebuild_failed")
	}
}
