package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"sync"
)

type semanticPrepared struct {
	exposure  *StateExposure
	values    []*Object
	port      *semanticQueryPort
	delivered bool
}
type semanticTurn struct {
	mu                    sync.Mutex
	entries               []*semanticPrepared
	tail                  *botruntime.ToolResultGuard
	delivering, reentered bool
}
type SemanticExposure struct{ port *semanticModelPort }

func NewSemanticExposure(memory *ExposurePort, policy *modelread.RuntimePolicy, now func() int64) (*SemanticExposure, error) {
	if memory == nil || policy == nil || now == nil {
		return nil, errors.New("semantic_exposure_owners_required")
	}
	return &SemanticExposure{&semanticModelPort{memory, policy, now}}, nil
}
func (e *SemanticExposure) turn(c *botruntime.TurnContext) *semanticTurn {
	return c.OwnedExtensionState(e, func() any { return &semanticTurn{} }).(*semanticTurn)
}
func (e *SemanticExposure) CaptureGuard(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	s := e.turn(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tail
}
func (e *SemanticExposure) Query(ctx context.Context, c *botruntime.TurnContext) ([]*Object, error) {
	empty := []*Object{}
	if !stateReal(c) {
		return empty, nil
	}
	prior := e.CaptureGuard(c)
	p := &semanticQueryPort{owner: e, turn: e.turn(c), prior: prior}
	allowed := false
	err := e.port.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		var er error
		allowed, er = e.port.allowed(reads, exposureContext(c))
		if er != nil || !allowed {
			return er
		}
		if prior != nil {
			allowed, er = prior.Check(reads)
			if er != nil || !allowed {
				return er
			}
		}
		p.raw, er = e.port.load(reads, exposureContext(c))
		if er != nil {
			return er
		}
		p.raw = exposureClone(p.raw).(*Object)
		p.policy, er = e.port.revision(reads)
		if er != nil {
			return er
		}
		p.first, er = semanticSnapshot(c.Scene.ID, p.raw)
		return er
	})
	if err != nil || !allowed {
		return empty, err
	}
	state, err := NewStateExposure(p, nil)
	if err != nil {
		return nil, err
	}
	values, err := state.Query(ctx, c, nil)
	if err != nil || len(values) == 0 {
		return values, err
	}
	turn := e.turn(c)
	turn.mu.Lock()
	turn.entries = append(turn.entries, &semanticPrepared{exposure: state, values: values, port: p})
	turn.mu.Unlock()
	return values, nil
}
func (e *SemanticExposure) Deliver(ctx context.Context, c *botruntime.TurnContext, values []any) ([]*Object, error) {
	empty := []*Object{}
	s := e.turn(c)
	s.mu.Lock()
	if s.delivering {
		s.reentered = true
		s.mu.Unlock()
		rejectExposure(c)
		return empty, nil
	}
	if len(values) == 0 {
		s.mu.Unlock()
		return empty, nil
	}
	first, _ := values[0].(*Object)
	var entry *semanticPrepared
	for _, p := range s.entries {
		if len(p.values) > 0 && p.values[0] == first {
			entry = p
			break
		}
	}
	if entry == nil {
		s.mu.Unlock()
		return empty, nil
	}
	s.delivering = true
	if !entry.delivered {
		entry.port.mu.Lock()
		entry.port.prior = s.tail
		entry.port.mu.Unlock()
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.delivering = false; s.mu.Unlock() }()
	out, err := entry.exposure.Deliver(ctx, c, values)
	if err != nil {
		return nil, err
	}
	if len(out) > 0 {
		s.mu.Lock()
		if !entry.delivered {
			entry.delivered = true
			s.tail = entry.exposure.CaptureGuard(c)
		}
		s.mu.Unlock()
	}
	return out, nil
}

type semanticQueryPort struct {
	mu         sync.Mutex
	owner      *SemanticExposure
	turn       *semanticTurn
	raw, first *Object
	policy     uint64
	prior      *botruntime.ToolResultGuard
}

func (p *semanticQueryPort) Query(context.Context, Context, any) ([]*Object, error) {
	return []*Object{p.first}, nil
}
func (p *semanticQueryPort) SourceIDs(values []*Object) ([]string, error) {
	return semanticSourceIDs(p.raw, values)
}
func (p *semanticQueryPort) Valid(ctx context.Context, c Context, values []*Object) (valid bool, err error) {
	err = p.owner.port.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { valid, err = p.ValidScoped(reads, c, values); return err })
	return
}

// Source valid catches authorization/projection failures and returns false. A
// detached send guard borrows its current reader, never the original turn ctx.
func (p *semanticQueryPort) ValidScoped(reads botruntime.ReadAuthority, c Context, values []*Object) (valid bool, err error) {
	defer func() {
		if recover() != nil {
			valid = false
			err = nil
		}
		if err != nil {
			valid = false
			err = nil
		}
	}()
	p.mu.Lock()
	prior := p.prior
	p.mu.Unlock()
	policy := func() (bool, error) {
		ok, e := p.owner.port.allowed(reads, c)
		if e != nil || !ok {
			return false, e
		}
		revision, e := p.owner.port.revision(reads)
		return revision == p.policy, e
	}
	if ok, e := policy(); e != nil || !ok {
		return false, e
	}
	if len(values) != 1 {
		return false, nil
	}
	raw, e := p.owner.port.load(reads, c)
	if e != nil {
		return false, e
	}
	if !deepEqual(raw, p.raw) || !deepEqual(values[0], p.first) {
		return false, nil
	}
	if prior != nil {
		ok, e := prior.Check(reads)
		if e != nil || !ok {
			return false, e
		}
	}
	if ok, e := policy(); e != nil || !ok {
		return false, e
	}
	if prior != nil {
		raw, e = p.owner.port.load(reads, c)
		if e != nil || !deepEqual(raw, p.raw) {
			return false, e
		}
	}
	if ok, e := policy(); e != nil || !ok {
		return false, e
	}
	p.turn.mu.Lock()
	defer p.turn.mu.Unlock()
	return !p.turn.reentered, nil
}
