package memoryservice

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"weak"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
)

// StateExposurePort is implemented by concrete current-state owners. Scoped
// validity must reuse the supplied authority and may not refresh or reopen roots.
type StateExposurePort interface {
	Query(context.Context, Context, any) ([]*Object, error)
	Valid(context.Context, Context, []*Object) (bool, error)
	ValidScoped(botruntime.ReadAuthority, Context, []*Object) (bool, error)
	SourceIDs([]*Object) ([]string, error)
}
type stateGroup struct {
	receipts  []weak.Pointer[Object]
	snapshots []*Object
	kept      []*Object
	delivered bool
}
type stateExposureTurn struct {
	operation sync.Mutex
	mu        sync.Mutex
	pending   map[weak.Pointer[Object]]*stateGroup
	delivered []*stateGroup
}
type stateReceiptCleanup struct {
	state weak.Pointer[stateExposureTurn]
	key   weak.Pointer[Object]
}

func releaseStateReceipt(item stateReceiptCleanup) {
	if state := item.state.Value(); state != nil {
		state.mu.Lock()
		delete(state.pending, item.key)
		state.mu.Unlock()
	}
}

type StateExposure struct {
	port        StateExposurePort
	onDelivered func(context.Context, *botruntime.TurnContext, []*Object) error
}

func NewStateExposure(port StateExposurePort, onDelivered func(context.Context, *botruntime.TurnContext, []*Object) error) (*StateExposure, error) {
	if port == nil {
		return nil, errors.New("state_exposure_port_required")
	}
	return &StateExposure{port, onDelivered}, nil
}
func (e *StateExposure) turn(c *botruntime.TurnContext) *stateExposureTurn {
	return c.OwnedExtensionState(e, func() any { return &stateExposureTurn{pending: map[weak.Pointer[Object]]*stateGroup{}} }).(*stateExposureTurn)
}
func stateReal(c *botruntime.TurnContext) bool { return c.Mode == "real" && !exposureAborted(c) }
func stateCopies(values []*Object) []*Object {
	out := make([]*Object, len(values))
	for i, v := range values {
		out[i] = exposureClone(v).(*Object)
	}
	return out
}
func (e *StateExposure) valid(ctx context.Context, c *botruntime.TurnContext, values []*Object) (bool, error) {
	if !stateReal(c) {
		return false, nil
	}
	return e.port.Valid(ctx, exposureContext(c), values)
}
func (e *StateExposure) Query(ctx context.Context, c *botruntime.TurnContext, input any) ([]*Object, error) {
	empty := []*Object{}
	if !stateReal(c) {
		return empty, nil
	}
	state := e.turn(c)
	state.operation.Lock()
	defer state.operation.Unlock()
	values, err := e.port.Query(ctx, exposureContext(c), input)
	if err != nil {
		return nil, err
	}
	values = stateCopies(values)
	if len(values) == 0 {
		return empty, nil
	}
	ok, err := e.valid(ctx, c, values)
	if err != nil || !ok {
		return empty, err
	}
	group := &stateGroup{receipts: make([]weak.Pointer[Object], len(values)), snapshots: stateCopies(values)}
	state.mu.Lock()
	for i, body := range values {
		key := weak.Make(body)
		group.receipts[i] = key
		state.pending[key] = group
		runtime.AddCleanup(body, releaseStateReceipt, stateReceiptCleanup{weak.Make(state), key})
	}
	state.mu.Unlock()
	return values, nil
}
func stateBodiesMatch(group *stateGroup, values []*Object) bool {
	if len(group.snapshots) != len(values) {
		return false
	}
	for i, v := range values {
		if v == nil || weak.Make(v) != group.receipts[i] || !deepEqual(v, group.snapshots[i]) {
			return false
		}
	}
	return true
}
func (e *StateExposure) Deliver(ctx context.Context, c *botruntime.TurnContext, selected []any) ([]*Object, error) {
	empty := []*Object{}
	if len(selected) == 0 || !stateReal(c) {
		return empty, nil
	}
	state := e.turn(c)
	state.operation.Lock()
	defer state.operation.Unlock()
	bodies := make([]*Object, len(selected))
	for i, v := range selected {
		bodies[i], _ = v.(*Object)
	}
	if bodies[0] == nil {
		return empty, nil
	}
	state.mu.Lock()
	group := state.pending[weak.Make(bodies[0])]
	state.mu.Unlock()
	if group == nil || !stateBodiesMatch(group, bodies) {
		return empty, nil
	}
	ok, err := e.valid(ctx, c, group.snapshots)
	if err != nil || !ok {
		return empty, err
	}
	if group.delivered {
		return append([]*Object{}, group.kept...), nil
	}
	ids, err := e.port.SourceIDs(group.snapshots)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if id == "" {
			return nil, errors.New("state_exposure_source_invalid")
		}
	}
	err = func() (err error) {
		defer func() {
			if v := recover(); v != nil {
				rejectExposure(c)
				panic(v)
			}
		}()
		if e.onDelivered != nil {
			if err = e.onDelivered(ctx, c, append([]*Object{}, bodies...)); err != nil {
				rejectExposure(c)
				return err
			}
		}
		ok, err = e.valid(ctx, c, group.snapshots)
		if err != nil {
			rejectExposure(c)
			return err
		}
		if !ok || !stateBodiesMatch(group, bodies) {
			rejectExposure(c)
			ok = false
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	if !ok {
		return empty, nil
	}
	state.mu.Lock()
	group.delivered = true
	group.kept = append([]*Object{}, bodies...)
	state.delivered = append(state.delivered, group)
	state.mu.Unlock()
	for _, id := range ids {
		c.AddDependency(id)
	}
	scope := exposureContext(c)
	port := e.port
	snapshots := stateCopies(group.snapshots)
	c.AddScopedValidation(func(reads botruntime.ReadAuthority) (bool, error) {
		if !stateReal(c) {
			return false, nil
		}
		valid, err := port.ValidScoped(reads, scope, snapshots)
		return valid && stateReal(c), err
	})
	return bodies, nil
}
func (e *StateExposure) ReadDelivered(ctx context.Context, c *botruntime.TurnContext) ([]*Object, error) {
	state := e.turn(c)
	state.mu.Lock()
	groups := append([]*stateGroup{}, state.delivered...)
	state.mu.Unlock()
	out := []*Object{}
	for _, group := range groups {
		ok, err := e.valid(ctx, c, group.snapshots)
		if err != nil || !ok {
			return []*Object{}, err
		}
		if !stateBodiesMatch(group, group.kept) {
			return []*Object{}, nil
		}
		out = append(out, group.kept...)
	}
	return out, nil
}

// HasExposure records actual delivery, not current validity. An invalidated
// delivery still supplies a false guard and cannot evade downstream gates.
func (e *StateExposure) HasExposure(c *botruntime.TurnContext) bool {
	state := e.turn(c)
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.delivered) > 0
}
func (e *StateExposure) CaptureGuard(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	state := e.turn(c)
	state.mu.Lock()
	groups := make([][]*Object, len(state.delivered))
	for i, g := range state.delivered {
		groups[i] = stateCopies(g.snapshots)
	}
	state.mu.Unlock()
	if len(groups) == 0 {
		return nil
	}
	port := e.port
	scope := exposureContext(c)
	real := c.Mode == "real"
	return &botruntime.ToolResultGuard{Check: func(reads botruntime.ReadAuthority) (bool, error) {
		if !real || scope.Simulation {
			return false, nil
		}
		for _, values := range groups {
			valid, err := port.ValidScoped(reads, scope, values)
			if err != nil || !valid {
				return false, err
			}
		}
		return true, nil
	}}
}
