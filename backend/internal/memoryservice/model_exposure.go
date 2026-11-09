package memoryservice

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"weak"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
)

type ExposedMemorySnapshot struct {
	Reference
	Deleted bool `json:"deleted"`
}
type preparedMemory struct {
	authority, body *Object
	reference       *ExposedMemorySnapshot
	binding         string
}
type memoryExposureState struct {
	operation          sync.Mutex
	mu                 sync.Mutex
	pending            map[weak.Pointer[Object]]*preparedMemory
	delivered          map[string]*preparedMemory
	exposed            map[string]ExposedMemorySnapshot
	order              []string
	validationAttached bool
	hasPending         bool
}

type pendingMemoryKey struct {
	state weak.Pointer[memoryExposureState]
	key   weak.Pointer[Object]
}

func releasePreparedMemory(item pendingMemoryKey) {
	state := item.state.Value()
	if state == nil {
		return
	}
	state.mu.Lock()
	delete(state.pending, item.key)
	state.mu.Unlock()
}

// ModelMemoryExposure binds returned object identities to immutable authority
// snapshots. Only selected, entirely valid receipts add dependencies. Its state
// belongs to the turn, never a process map keyed by a caller-supplied turn ID.
type ModelMemoryExposure struct {
	port        *ExposurePort
	onDelivered func(context.Context, *botruntime.TurnContext, []Reference) error
}

func NewModelMemoryExposure(port *ExposurePort, onDelivered func(context.Context, *botruntime.TurnContext, []Reference) error) (*ModelMemoryExposure, error) {
	if port == nil || onDelivered == nil {
		return nil, errors.New("memory_exposure_delivery_owner_required")
	}
	return &ModelMemoryExposure{port, onDelivered}, nil
}
func (e *ModelMemoryExposure) turn(c *botruntime.TurnContext) *memoryExposureState {
	return c.OwnedExtensionState(e, func() any {
		return &memoryExposureState{pending: map[weak.Pointer[Object]]*preparedMemory{}, delivered: map[string]*preparedMemory{}, exposed: map[string]ExposedMemorySnapshot{}}
	}).(*memoryExposureState)
}
func exposureContext(c *botruntime.TurnContext) Context {
	return Context{SceneID: c.Scene.ID, Simulation: c.Mode == "simulation"}
}
func exposureAborted(c *botruntime.TurnContext) bool {
	return c.Signal() != nil && c.Signal().Aborted()
}
func exposureClone(v any) any {
	switch x := v.(type) {
	case *Object:
		if x == nil {
			return (*Object)(nil)
		}
		o := new(Object)
		for _, m := range x.Members() {
			_ = o.Set(m.Key, exposureClone(m.Value))
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = exposureClone(v)
		}
		return out
	default:
		return v
	}
}
func authorityBody(record *Object) *Object {
	body := exposureClone(record).(*Object)
	body.Delete("score")
	return body
}
func (e *ModelMemoryExposure) Prepare(ctx context.Context, c *botruntime.TurnContext, records []*Object) ([]*Object, error) {
	out := []*Object{}
	if exposureAborted(c) {
		return out, nil
	}
	state := e.turn(c)
	state.operation.Lock()
	defer state.operation.Unlock()
	bodies := map[string]*Object{}
	ids := make([]string, len(records))
	for i, record := range records {
		if record == nil {
			return nil, errors.New("memory_record_required")
		}
		id := text(record, "id")
		body := authorityBody(record)
		if prior := bodies[id]; prior != nil && !deepEqual(prior, body) {
			return out, nil
		}
		bodies[id] = body
		ids[i] = id
	}
	scope := exposureContext(c)
	refs := map[string]Reference{}
	bindings := map[string]string{}
	var current map[string]ExposureSnapshot
	if !scope.Simulation {
		batch, err := e.port.Capture(ctx, scope, records)
		if err != nil {
			return nil, err
		}
		bindingIDs := []string{}
		for _, ref := range batch {
			if _, ok := refs[ref.ID]; !ok {
				bindingIDs = append(bindingIDs, ref.ID)
			}
			refs[ref.ID] = ref
		}
		bindings, err = e.port.Bindings(ctx, bindingIDs)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		current, err = e.port.Snapshot(ctx, scope, ids)
		if err != nil {
			return nil, err
		}
	}
	pending := map[*Object]*preparedMemory{}
	for _, record := range records {
		id := text(record, "id")
		authority := bodies[id]
		var snapshot *ExposedMemorySnapshot
		if scope.Simulation {
			if !deepEqual(current[id].Record, authority) {
				continue
			}
		} else {
			ref, ok := refs[id]
			if !ok {
				continue
			}
			snapshot = &ExposedMemorySnapshot{Reference: ref}
		}
		body := exposureClone(record).(*Object)
		binding := bindings[id]
		if binding != "" {
			_ = body.Set("parentBinding", binding)
		}
		// The caller-visible object is the receipt identity. Retain separate deep
		// snapshots because native Object cannot enforce JavaScript Object.freeze.
		pending[body] = &preparedMemory{authority: exposureClone(authority).(*Object), body: exposureClone(body).(*Object), reference: snapshot, binding: binding}
		out = append(out, body)
	}
	state.mu.Lock()
	state.hasPending = true
	for body, prepared := range pending {
		key := weak.Make(body)
		state.pending[key] = prepared
		runtime.AddCleanup(body, releasePreparedMemory, pendingMemoryKey{weak.Make(state), key})
	}
	state.mu.Unlock()
	return out, nil
}
func validPrepared(scope Context, p *preparedMemory, current map[string]ExposureSnapshot) bool {
	now, ok := current[text(p.authority, "id")]
	if !ok || !deepEqual(now.Record, p.authority) {
		return false
	}
	if scope.Simulation {
		return true
	}
	return p.reference != nil && !now.State.Deleted && now.State.Version == p.reference.Version && now.State.DeletionEpoch == p.reference.DeletionEpoch && now.Binding == p.binding
}
func rejectExposure(c *botruntime.TurnContext) {
	c.AddScopedValidation(func(botruntime.ReadAuthority) (bool, error) { return false, nil })
}
func (e *ModelMemoryExposure) Commit(ctx context.Context, c *botruntime.TurnContext, selected []any) ([]*Object, error) {
	empty := []*Object{}
	if exposureAborted(c) {
		return empty, nil
	}
	state := e.turn(c)
	state.operation.Lock()
	defer state.operation.Unlock()
	state.mu.Lock()
	if !state.hasPending {
		state.mu.Unlock()
		return empty, nil
	}
	accepted := make([]*preparedMemory, len(selected))
	selectedBodies := make([]*Object, len(selected))
	ids := []string{}
	previous := make(map[string]ExposedMemorySnapshot, len(selected))
	for i, v := range selected {
		body, _ := v.(*Object)
		selectedBodies[i] = body
		if body != nil {
			accepted[i] = state.pending[weak.Make(body)]
			if accepted[i] != nil {
				id := text(accepted[i].authority, "id")
				ids = append(ids, id)
				if snapshot, ok := state.exposed[id]; ok {
					previous[id] = snapshot
				}
			}
		}
	}
	state.mu.Unlock()
	scope := exposureContext(c)
	current, err := e.port.Snapshot(ctx, scope, ids)
	if err != nil {
		return nil, err
	}
	for i, p := range accepted {
		if p == nil || !deepEqual(selectedBodies[i], p.body) || exposureAborted(c) || !validPrepared(scope, p, current) {
			return empty, nil
		}
		if !scope.Simulation {
			if prior, ok := previous[p.reference.ID]; ok && prior != *p.reference {
				rejectExposure(c)
				return empty, nil
			}
		}
	}
	if !scope.Simulation {
		refs := make([]Reference, len(accepted))
		for i, p := range accepted {
			refs[i] = p.reference.Reference
		}
		// This callback may write. It is deliberately outside every publication and
		// read scope; requalification afterwards cannot reuse an old authorized map.
		if err = func() (err error) {
			defer func() {
				if cause := recover(); cause != nil {
					rejectExposure(c)
					panic(cause)
				}
			}()
			return e.onDelivered(ctx, c, refs)
		}(); err != nil {
			rejectExposure(c)
			return nil, err
		}
		current, err = e.port.Snapshot(ctx, scope, ids)
		if err != nil {
			return nil, err
		}
		for i, p := range accepted {
			if exposureAborted(c) || !deepEqual(selectedBodies[i], p.body) || !validPrepared(scope, p, current) {
				rejectExposure(c)
				return empty, nil
			}
		}
	}
	attach := false
	dependencies := []string{}
	if !scope.Simulation {
		state.mu.Lock()
		for _, p := range accepted {
			ref := *p.reference
			if _, ok := state.exposed[ref.ID]; ok {
				continue
			}
			state.exposed[ref.ID] = ref
			state.delivered[ref.ID] = p
			state.order = append(state.order, ref.ID)
			if !state.validationAttached {
				state.validationAttached = true
				attach = true
			}
			sources, _ := value(p.authority, "sources").([]any)
			for _, v := range sources {
				source := object(v)
				if text(source, "sceneId") == c.Scene.ID {
					dependencies = append(dependencies, text(source, "messageId"))
				}
			}
		}
		state.mu.Unlock()
	}
	for _, id := range dependencies {
		c.AddDependency(id)
	}
	if attach {
		c.AddScopedValidation(func(reads botruntime.ReadAuthority) (bool, error) {
			if exposureAborted(c) {
				return false, nil
			}
			state.mu.Lock()
			items := make([]*preparedMemory, 0, len(state.order))
			ids := append([]string{}, state.order...)
			for _, id := range ids {
				items = append(items, state.delivered[id])
			}
			state.mu.Unlock()
			current, err := e.port.SnapshotScoped(reads, scope, ids)
			if err != nil {
				return false, err
			}
			for _, p := range items {
				if !validPrepared(scope, p, current) {
					return false, nil
				}
			}
			return !exposureAborted(c), nil
		})
	}
	return selectedBodies, nil
}
func (e *ModelMemoryExposure) ReadExposed(c *botruntime.TurnContext) []ExposedMemorySnapshot {
	state := e.turn(c)
	state.mu.Lock()
	defer state.mu.Unlock()
	out := make([]ExposedMemorySnapshot, 0, len(state.order))
	for _, id := range state.order {
		out = append(out, state.exposed[id])
	}
	return out
}

// CaptureExactGuard retains only the first delivered immutable body/ref and
// scene. It never captures the old turn, its context, signal, or a read scope.
func (e *ModelMemoryExposure) CaptureExactGuard(c *botruntime.TurnContext, id string) *botruntime.ToolResultGuard {
	if c.Mode != "real" {
		return nil
	}
	state := e.turn(c)
	state.mu.Lock()
	p := state.delivered[id]
	state.mu.Unlock()
	if p == nil || p.reference == nil {
		return nil
	}
	ref := *p.reference
	copy := &preparedMemory{authority: exposureClone(p.authority).(*Object), body: exposureClone(p.body).(*Object), reference: &ref, binding: p.binding}
	scope := exposureContext(c)
	port := e.port
	return &botruntime.ToolResultGuard{Check: func(reads botruntime.ReadAuthority) (valid bool, err error) {
		// Source captureExactGuard catches authorization failures. It never
		// upgrades the exact reference or promotes an error to permission.
		defer func() {
			if recover() != nil {
				valid, err = false, nil
			}
		}()
		current, err := port.SnapshotScoped(reads, scope, []string{id})
		if err != nil {
			return false, nil
		}
		return validPrepared(scope, copy, current), nil
	}}
}

// ComposeExtension preserves all inherited behavior. Install the result in
// place of base, or pass an empty base when registering an independent owner.
// Execute/search/model-policy gating remains the Engine composition's job.
func (e *ModelMemoryExposure) ComposeExtension(base botruntime.Extension) *botruntime.Extension {
	ext := base
	ext.CaptureToolResultGuards = func(c *botruntime.TurnContext) []*botruntime.ToolResultGuard {
		out := []*botruntime.ToolResultGuard{}
		if base.CaptureToolResultGuards != nil {
			out = append(out, base.CaptureToolResultGuards(c)...)
		}
		for _, ref := range e.ReadExposed(c) {
			if g := e.CaptureExactGuard(c, ref.ID); g != nil {
				out = append(out, g)
			}
		}
		return out
	}
	ext.Delivered = func(name string, v any, c *botruntime.TurnContext, input any) (any, error) {
		if name == "memory.search" || name == "memory.person" {
			var selected []any
			switch values := v.(type) {
			case []any:
				selected = values
			case []*Object:
				selected = make([]any, len(values))
				for i, v := range values {
					selected[i] = v
				}
			}
			if selected != nil {
				out, err := e.Commit(context.Background(), c, selected)
				if err != nil {
					return nil, err
				}
				items := make([]any, len(out))
				for i, v := range out {
					items[i] = v
				}
				v = items
			}
		}
		if base.Delivered != nil {
			return base.Delivered(name, v, c, input)
		}
		return v, nil
	}
	return &ext
}
