package memoryservice

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/semantic"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
	"math"
	"sync"
)

type SemanticRuntime struct {
	Memory           *Service
	Learning         *semantic.Coordinator
	main             *sql.DB
	authority        *recovery.Authority
	foreground       *botruntime.Runtime
	logs             *observability.Logger
	mu               sync.Mutex
	closed, draining bool
	drained          chan struct{}
	pending          map[string]semanticQueued
	nextNotification uint64
	order            []string
	sessions         sync.Map
	rootWork         sync.WaitGroup
	unsubscribe      func()
}
type SemanticRuntimeDependencies struct {
	Memory      Dependencies
	Sources     *sourceevents.Store
	Main        *sql.DB
	Authority   *recovery.Authority
	Models      *providertransport.NativeModels
	Read        *modelread.Policy
	Foreground  *botruntime.Runtime
	Logs        *observability.Logger
	Maintenance *workingset.Maintenance
	Options     semantic.CoordinatorOptions
}

// NewSemanticRuntime installs actual qualification, episode graph, candidate,
// receipt and post-stack notification owners around the existing domain stores.
// Application ownership is explicit: dependencies are borrowed, never closed.
func NewSemanticRuntime(d SemanticRuntimeDependencies) (*SemanticRuntime, error) {
	if d.Sources == nil || d.Memory.Assertions == nil || d.Main == nil || d.Authority == nil || d.Foreground == nil || d.Logs == nil || d.Maintenance == nil || d.Read == nil || d.Models == nil {
		return nil, errors.New("semantic_runtime_owners_required")
	}
	if e := d.Memory.Assertions.CheckReadBinding(d.Authority, d.Main, nil); e != nil {
		return nil, e
	}
	if e := d.Read.CheckBinding(d.Authority, d.Main); e != nil {
		return nil, e
	}
	if e := d.Sources.WithRuntimeReads(context.Background(), func(reads botruntime.ReadAuthority) error {
		return d.Sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error { return scope.CheckBinding(d.Main, d.Authority) })
	}); e != nil {
		return nil, e
	}
	r := &SemanticRuntime{main: d.Main, authority: d.Authority, foreground: d.Foreground, logs: d.Logs, pending: map[string]semanticQueued{}, drained: make(chan struct{})}
	close(r.drained)
	d.Memory.Notifications = r
	d.Memory.Dirty = d.Maintenance.Notify
	d.Memory.DirtyInSession = d.Maintenance.NotifyInSession
	d.Memory.Log = func(level, category, message string) {
		_ = d.Logs.Emit(observability.Event{Level: level, Module: category, Code: message, Message: message})
	}
	memory, e := New(d.Memory)
	if e != nil {
		return nil, e
	}
	r.Memory = memory
	r.Learning, e = semantic.NewCoordinator(r, d.Read, d.Models, d.Options)
	if e != nil {
		return nil, e
	}
	r.unsubscribe = d.Sources.SubscribeCommitted(&sourceevents.Listener{Handle: r.sourceChanged})
	return r, nil
}
func (r *SemanticRuntime) state(ctx context.Context, scene string) (out semanticState, err error) {
	err = r.authority.Publish(ctx, r.main, func(conn *sql.Conn) ([]byte, error) {
		var e error
		out, e = semanticReadState(func(q string, args ...any) (semanticRows, error) { return conn.QueryContext(ctx, q, args...) }, scene)
		return nil, e
	}, func([]byte) error { return nil })
	return
}
func (r *SemanticRuntime) Enabled(scene string) bool {
	state, e := r.state(context.Background(), scene)
	return e == nil && state.enabled()
}
func (r *SemanticRuntime) PolicyRevision() uint64 {
	state, e := r.state(context.Background(), "")
	if e != nil {
		return math.MaxUint64
	}
	return state.revision
}
func (r *SemanticRuntime) ForegroundBusy() bool { return r.foreground.ActiveTasks() > 0 }
func (r *SemanticRuntime) Timezone() string {
	state, e := r.state(context.Background(), "")
	if e != nil {
		return ""
	}
	v, _ := state.settings["timezone"].(string)
	return v
}
func (r *SemanticRuntime) ResolveDirty(ctx context.Context, d []semantic.Dirty, t *modelread.Ticket) (*semantic.ResolvedInput, error) {
	return r.Memory.ResolveSemantic(ctx, d, t)
}
func (r *SemanticRuntime) Valid(ctx context.Context, s *semantic.Input) bool {
	valid, e := r.Memory.ValidSemantic(ctx, s)
	return e == nil && valid
}
func (r *SemanticRuntime) ValidInSession(ctx context.Context, s *semantic.Input, ws *recovery.WriteSession) bool {
	v, ok := r.sessions.Load(ws)
	if !ok {
		return false
	}
	valid, e := r.Memory.semanticNeighborhood(ctx, v.(*assertions.RevisionTxn), ws).valid(s)
	return e == nil && valid
}
func (r *SemanticRuntime) PolicyInSession(ctx context.Context, scene string, revision uint64, ws *recovery.WriteSession) bool {
	if ctx.Err() != nil {
		return false
	}
	state, e := semanticSessionState(ws, scene)
	return e == nil && state.enabled() && state.revision == revision
}
func (r *SemanticRuntime) BeginAttempt(ctx context.Context, s *semantic.Input) (bool, error) {
	return r.Memory.BeginSemanticAttempt(ctx, s)
}
func (r *SemanticRuntime) FinishAttempt(ctx context.Context, s *semantic.Input, status string) error {
	return r.Memory.FinishSemanticAttempt(ctx, s, status)
}
func (r *SemanticRuntime) Commit(ctx context.Context, s *semantic.Input, proposals []*Object, guard func(*recovery.WriteSession) error) (semantic.CommitResult, error) {
	return r.Memory.commitSemantic(ctx, s, proposals, func(tx *assertions.RevisionTxn, ws *recovery.WriteSession) error {
		r.sessions.Store(ws, tx)
		defer r.sessions.Delete(ws)
		return guard(ws)
	})
}
func (r *SemanticRuntime) Event(e semantic.Event) {
	fields := map[string]any{}
	if e.Reason != "" {
		fields["reason"] = e.Reason
	}
	_ = r.logs.Emit(observability.Event{Level: "info", Module: "memory", Code: "semantic_" + e.Status, Message: "semantic_" + e.Status, SceneID: e.SceneID, Fields: fields})
}
func (r *SemanticRuntime) ConfigureSemantic(ctx context.Context) error {
	return r.Learning.Configure(ctx, nil)
}
func (r *SemanticRuntime) CancelScene(scene string) { r.Learning.CancelScene(scene) }
func (r *SemanticRuntime) notificationFailure() {
	_ = r.logs.Emit(observability.Event{Level: "info", Module: "memory", Code: "semantic_notification_unavailable", Message: "semantic_notification_unavailable"})
}

type semanticQueued struct {
	notification
	generation uint64
	ready      bool
}

func (r *SemanticRuntime) enqueue(scene string, ref Reference, reason string, state semanticState, ready bool) (string, uint64, error) {
	if !state.enabled() {
		return "", 0, nil
	}
	policy, e := state.identity()
	if e != nil {
		return "", 0, e
	}
	key, e := jsjson.Stringify([]any{scene, refObject(ref)})
	if e != nil {
		return "", 0, e
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", 0, nil
	}
	if _, exists := r.pending[key]; !exists {
		if len(r.pending) >= 1024 {
			r.mu.Unlock()
			r.notificationFailure()
			return "", 0, nil
		}
		r.order = append(r.order, key)
	}
	r.nextNotification++
	generation := r.nextNotification
	r.pending[key] = semanticQueued{notification{scene, ref, reason, policy}, generation, ready}
	if !ready {
		r.rootWork.Add(1)
	}
	r.mu.Unlock()
	return key, generation, nil
}
func (r *SemanticRuntime) AssertionCommittedInSession(ws *recovery.WriteSession, scene string, ref Reference) error {
	return r.NotifyInSession(ws, scene, ref, "assertion_committed")
}
func (r *SemanticRuntime) NotifyInSession(ws *recovery.WriteSession, scene string, ref Reference, reason string) error {
	state, e := semanticSessionState(ws, scene)
	if e != nil {
		return e
	}
	key, generation, e := r.enqueue(scene, ref, reason, state, false)
	if e != nil || key == "" {
		return e
	}
	if e = ws.AfterRoot(func(outcome recovery.RootOutcome) {
		defer r.rootWork.Done()
		r.mu.Lock()
		if queued, ok := r.pending[key]; ok && queued.generation == generation {
			if outcome.Status == recovery.RootUncertain {
				delete(r.pending, key)
			} else {
				queued.ready = true
				r.pending[key] = queued
			}
		}
		r.mu.Unlock()
		if outcome.Status == recovery.RootUncertain {
			r.notificationFailure()
		}
		r.AfterMemoryStack()
	}); e != nil {
		r.rootWork.Done()
		r.mu.Lock()
		if q, ok := r.pending[key]; ok && q.generation == generation {
			delete(r.pending, key)
		}
		r.mu.Unlock()
		return e
	}
	return nil
}
func (r *SemanticRuntime) AssertionCommitted(scene string, ref Reference) error {
	state, e := r.state(context.Background(), scene)
	if e != nil {
		return e
	}
	if _, _, e = r.enqueue(scene, ref, "assertion_committed", state, true); e != nil {
		return e
	}
	r.AfterMemoryStack()
	return nil
}

// AfterMemoryStack is called only after the outer revision transaction unwinds,
// including rollback. Delivery is serialized, never a competing transaction
// spawned from a checkpoint. Reentrant refresh notifications form the next batch.
func (r *SemanticRuntime) AfterMemoryStack() {
	r.mu.Lock()
	if r.closed || r.draining || len(r.order) == 0 {
		r.mu.Unlock()
		return
	}
	r.draining = true
	r.drained = make(chan struct{})
	r.mu.Unlock()
	for {
		r.mu.Lock()
		batch := []notification{}
		remaining := []string{}
		if !r.closed {
			for _, key := range r.order {
				q, ok := r.pending[key]
				if !ok {
					continue
				}
				if q.ready {
					batch = append(batch, q.notification)
					delete(r.pending, key)
				} else {
					remaining = append(remaining, key)
				}
			}
		}
		r.order = remaining
		if r.closed || len(batch) == 0 {
			r.draining = false
			close(r.drained)
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		for _, n := range batch {
			if e := r.deliverNotification(n); e != nil {
				r.notificationFailure()
			}
		}
	}
}
func (r *SemanticRuntime) deliverNotification(n notification) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("semantic_notification_unavailable")
		}
	}()
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil
	}
	ctx := context.Background()
	state, e := r.state(ctx, n.scene)
	if e != nil {
		return e
	}
	identity, e := state.identity()
	if e != nil {
		return e
	}
	if !state.enabled() || identity != n.policy {
		return nil
	}
	current, e := r.Memory.SemanticCurrent(ctx, Context{SceneID: n.scene}, n.ref)
	if e != nil {
		return e
	}
	if current != nil && value(current, "derived") == false && text(current, "rootCoverage") == "complete" {
		var person *string
		scope := object(epGet(value(current, "record"), "scope"))
		if text(scope, "kind") == "scene" {
			if p, ok := value(scope, "personId").(string); ok {
				person = &p
			}
		}
		_, e = r.Learning.Notify(ctx, semantic.Dirty{SceneID: n.scene, PersonID: person, Trigger: epObj("ref", refObject(n.ref), "dependencyKey", value(current, "dependencyKey")), Reason: n.reason})
		if e != nil {
			return e
		}
	}
	page, e := r.Memory.SemanticEpisodes(ctx, Context{SceneID: n.scene}, n.ref, knowledge.ReadOptions{KnowledgeEnabled: state.settings["memoryKnowledgeEnabled"] == true, Authority: r.Memory.d.Authority})
	if e != nil {
		return e
	}
	for _, group := range epArr(value(page, "groups")) {
		parents := epArr(epGet(group, "parents"))
		if len(parents) == 0 {
			continue
		}
		_, e = r.Learning.Notify(ctx, semantic.Dirty{SceneID: n.scene, Trigger: object(parents[0]), EpisodeAnchor: object(epGet(group, "anchor")), Reason: n.reason})
		if e != nil {
			return e
		}
	}
	return nil
}
func (r *SemanticRuntime) CloseAndWait(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	r.pending = map[string]semanticQueued{}
	r.order = nil
	drained := r.drained
	r.mu.Unlock()
	if r.unsubscribe != nil {
		r.unsubscribe()
	}
	r.Learning.Dispose()
	roots := make(chan struct{})
	go func() { r.rootWork.Wait(); close(roots) }()
	select {
	case <-roots:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-drained:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.Learning.CloseAndWait(ctx)
}

func (r *SemanticRuntime) sourceChanged(store *sourceevents.Store, m sourceevents.Mutation) error {
	if m.Kind != "new" && m.Kind != "edited" {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.rootWork.Add(1)
	r.mu.Unlock()
	registered := false
	defer func() {
		if !registered {
			r.rootWork.Done()
		}
	}()
	return store.UseWriteSession(func(ws *recovery.WriteSession) error {
		if e := ws.AfterRoot(func(recovery.RootOutcome) { r.rootWork.Done() }); e != nil {
			return e
		}
		registered = true
		rows, e := ws.Query("SELECT DISTINCT memory_id AS id FROM memory_sources WHERE scene_id=? AND message_id=?", m.Identity.SceneID, m.Identity.MessageID)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		refs := []Reference{}
		e = r.Memory.d.Assertions.WithSession(ws).Read(context.Background(), func(reader *assertions.Reader) error {
			for _, id := range ids {
				state, e := reader.State(id)
				if e != nil {
					return e
				}
				if !state.Deleted && state.Version > 0 {
					refs = append(refs, Reference{id, state.Version, state.DeletionEpoch})
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
		workspace := []workingset.Reference{}
		for _, ref := range refs {
			workspace = append(workspace, workingset.Reference{Kind: "assertion", ID: ref.ID, Version: float64(ref.Version), DeletionEpoch: float64(ref.DeletionEpoch)})
		}
		result, e := r.Memory.mutations(ws).SourceEdited(workingset.Context{SceneID: m.Identity.SceneID}, workspace)
		if e != nil {
			return e
		}
		if result.Queued > 0 {
			r.Memory.notifyDirty(ws)
		}
		for _, ref := range refs {
			if e = r.NotifyInSession(ws, m.Identity.SceneID, ref, "source_qualification_changed"); e != nil {
				return e
			}
		}
		return nil
	})
}
