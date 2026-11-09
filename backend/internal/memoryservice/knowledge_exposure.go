package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"sync"
)

type knowledgePrepared struct {
	exposure  *StateExposure
	values    []*Object
	port      *knowledgeQueryPort
	delivered bool
}
type knowledgeTurn struct {
	mu                    sync.Mutex
	entries               []*knowledgePrepared
	tail                  *botruntime.ToolResultGuard
	delivering, reentered bool
}
type KnowledgeExposure struct{ port *knowledgeExposurePort }

func NewKnowledgeExposure(memory *ExposurePort, policy *modelread.RuntimePolicy, now func() int64) (*KnowledgeExposure, error) {
	if memory == nil || policy == nil || now == nil {
		return nil, errors.New("knowledge_exposure_owners_required")
	}
	return &KnowledgeExposure{&knowledgeExposurePort{memory, policy, now}}, nil
}
func (e *KnowledgeExposure) turn(c *botruntime.TurnContext) *knowledgeTurn {
	return c.OwnedExtensionState(e, func() any { return &knowledgeTurn{} }).(*knowledgeTurn)
}
func (e *KnowledgeExposure) CaptureGuard(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	s := e.turn(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tail
}
func (e *KnowledgeExposure) check(ctx context.Context, g *botruntime.ToolResultGuard) (valid bool, err error) {
	if g == nil {
		return true, nil
	}
	err = e.port.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { valid, err = g.Check(reads); return err })
	return
}
func (e *KnowledgeExposure) allowed(ctx context.Context, c Context) (valid bool, err error) {
	err = e.port.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { valid, err = e.port.allowed(reads, c); return err })
	return
}
func (e *KnowledgeExposure) Query(ctx context.Context, c *botruntime.TurnContext, query *Object) ([]*Object, error) {
	return e.query(ctx, c, query, nil, nil)
}
func (e *KnowledgeExposure) QueryFromBacking(ctx context.Context, c *botruntime.TurnContext, backing, query *Object, anchor *botruntime.ToolResultGuard) ([]*Object, error) {
	if anchor == nil {
		return []*Object{}, nil
	}
	return e.query(ctx, c, query, backing, anchor)
}
func (e *KnowledgeExposure) query(ctx context.Context, c *botruntime.TurnContext, query, backing *Object, anchor *botruntime.ToolResultGuard) ([]*Object, error) {
	empty := []*Object{}
	if !stateReal(c) {
		return empty, nil
	}
	ok, err := e.allowed(ctx, exposureContext(c))
	if err != nil || !ok {
		return empty, err
	}
	if anchor != nil {
		ok, err = e.checkAnchor(ctx, anchor)
		if err != nil || !ok {
			return empty, nil
		}
	}
	prior := e.CaptureGuard(c)
	if ok, err = e.check(ctx, prior); err != nil || !ok {
		return empty, err
	}
	entities, err := e.ReadEntities(ctx, c)
	if err != nil {
		return nil, err
	}
	q := exposureClone(query).(*Object)
	a := object(value(q, "anchor"))
	if text(a, "kind") == "entity" {
		found := false
		for _, raw := range entities {
			v := object(raw)
			if text(v, "entityId") == text(a, "entityId") && deepEqual(value(v, "backingRef"), value(a, "backingRef")) {
				found = true
				break
			}
		}
		if !found {
			return empty, nil
		}
	}
	p := &knowledgeQueryPort{owner: e, turn: e.turn(c), query: q, entities: entities, prior: prior, anchor: anchor}
	if backing != nil {
		p.backing = exposureClone(backing).(*Object)
	}
	err = e.port.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		var er error
		p.first, er = p.snapshot(reads, exposureContext(c))
		if er != nil {
			return er
		}
		p.policy, er = e.port.policyIdentity(reads)
		return er
	})
	if err != nil {
		return nil, err
	}
	state, err := NewStateExposure(p, nil)
	if err != nil {
		return nil, err
	}
	values, err := state.Query(ctx, c, nil)
	if err != nil || len(values) == 0 {
		return values, err
	}
	s := e.turn(c)
	s.mu.Lock()
	s.entries = append(s.entries, &knowledgePrepared{exposure: state, values: values, port: p})
	s.mu.Unlock()
	return values, nil
}
func (e *KnowledgeExposure) Deliver(ctx context.Context, c *botruntime.TurnContext, values []any) ([]*Object, error) {
	empty := []*Object{}
	if len(values) == 0 {
		return empty, nil
	}
	first, _ := values[0].(*Object)
	s := e.turn(c)
	s.mu.Lock()
	var entry *knowledgePrepared
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
	if s.delivering {
		s.reentered = true
		s.mu.Unlock()
		rejectExposure(c)
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
func (e *KnowledgeExposure) ReadDelivered(ctx context.Context, c *botruntime.TurnContext) ([]*Object, error) {
	ok, err := e.check(ctx, e.CaptureGuard(c))
	if err != nil || !ok {
		return []*Object{}, err
	}
	s := e.turn(c)
	s.mu.Lock()
	entries := append([]*knowledgePrepared{}, s.entries...)
	s.mu.Unlock()
	out := []*Object{}
	for _, p := range entries {
		values, err := p.exposure.ReadDelivered(ctx, c)
		if err != nil {
			return nil, err
		}
		out = append(out, values...)
	}
	return out, nil
}
func (e *KnowledgeExposure) ReadEntities(ctx context.Context, c *botruntime.TurnContext) ([]any, error) {
	values, err := e.ReadDelivered(ctx, c)
	if err != nil {
		return nil, err
	}
	out := []any{}
	seen := map[string]*Object{}
	for _, v := range values {
		edges, _ := value(object(value(v, "page")), "edges").([]any)
		for _, raw := range edges {
			edge := object(raw)
			for _, key := range []string{"subject", "object"} {
				endpoint := object(value(edge, key))
				if text(endpoint, "kind") != "entity" {
					continue
				}
				entity := object(value(endpoint, "entity"))
				if text(entity, "sceneId") != c.Scene.ID {
					return nil, errors.New("knowledge_exposure_scope_invalid")
				}
				id := text(entity, "id")
				ref := object(value(entity, "backing"))
				if prior, ok := seen[id]; ok {
					if !deepEqual(prior, ref) {
						return []any{}, nil
					}
					continue
				}
				seen[id] = ref
				out = append(out, relationObject("entityId", id, "backingRef", exposureClone(ref)))
			}
		}
	}
	return out, nil
}
func (e *KnowledgeExposure) ReadEdges(ctx context.Context, c *botruntime.TurnContext) ([]any, error) {
	values, err := e.ReadDelivered(ctx, c)
	if err != nil {
		return nil, err
	}
	out := []any{}
	seen := map[string]*Object{}
	for _, v := range values {
		page := object(value(v, "page"))
		edges, _ := value(page, "edges").([]any)
		support, _ := value(object(value(page, "supportClosure")), "edges").([]any)
		for _, raw := range append(append([]any{}, edges...), support...) {
			edge := object(raw)
			id := text(edge, "id")
			snapshot := relationObject("id", id, "version", value(edge, "version"), "backing", exposureClone(value(edge, "backing")))
			if old, ok := seen[id]; ok {
				if !deepEqual(old, snapshot) {
					return []any{}, nil
				}
				continue
			}
			seen[id] = snapshot
			out = append(out, snapshot)
		}
	}
	return out, nil
}

type knowledgeQueryPort struct {
	mu                    sync.Mutex
	owner                 *KnowledgeExposure
	turn                  *knowledgeTurn
	query, backing, first *Object
	entities              []any
	policy                float64
	prior, anchor         *botruntime.ToolResultGuard
}

func (p *knowledgeQueryPort) snapshot(reads botruntime.ReadAuthority, c Context) (*Object, error) {
	page, err := p.owner.port.load(reads, c, p.entities, p.query, p.backing)
	if err != nil {
		return nil, err
	}
	return knowledgeSnapshot(c.SceneID, commitmentNumber(p.query, "depth"), page)
}
func (p *knowledgeQueryPort) Query(context.Context, Context, any) ([]*Object, error) {
	return []*Object{p.first}, nil
}
func (p *knowledgeQueryPort) SourceIDs(v []*Object) ([]string, error) { return knowledgeSourceIDs(v) }
func (p *knowledgeQueryPort) Valid(ctx context.Context, c Context, values []*Object) (valid bool, err error) {
	err = p.owner.port.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { valid, err = p.ValidScoped(reads, c, values); return err })
	return
}
func (p *knowledgeQueryPort) ValidScoped(reads botruntime.ReadAuthority, c Context, values []*Object) (bool, error) {
	p.mu.Lock()
	prior := p.prior
	p.mu.Unlock()
	policy := func() (bool, error) {
		ok, err := p.owner.port.allowed(reads, c)
		if err != nil || !ok {
			return false, err
		}
		v, err := p.owner.port.policyIdentity(reads)
		return v == p.policy, err
	}
	anchor := func() bool {
		if p.anchor == nil {
			return true
		}
		return knowledgeAnchorValid(p.anchor, reads)
	}
	if ok, err := policy(); err != nil || !ok {
		return false, err
	}
	if !anchor() || len(values) != 1 {
		return false, nil
	}
	snapshot, err := p.snapshot(reads, c)
	if err != nil || !deepEqual(snapshot, values[0]) {
		return false, err
	}
	if !anchor() {
		return false, nil
	}
	if prior != nil {
		ok, err := prior.Check(reads)
		if err != nil || !ok {
			return false, err
		}
	}
	if ok, err := policy(); err != nil || !ok {
		return false, err
	}
	if !anchor() {
		return false, nil
	}
	if prior != nil {
		snapshot, err = p.snapshot(reads, c)
		if err != nil || !deepEqual(snapshot, values[0]) {
			return false, err
		}
	}
	if ok, err := policy(); err != nil || !ok {
		return false, err
	}
	if !anchor() {
		return false, nil
	}
	p.turn.mu.Lock()
	defer p.turn.mu.Unlock()
	return !p.turn.reentered, nil
}

func knowledgeAnchorValid(guard *botruntime.ToolResultGuard, reads botruntime.ReadAuthority) (valid bool) {
	defer func() {
		if recover() != nil {
			valid = false
		}
	}()
	valid, err := guard.Check(reads)
	return err == nil && valid
}
func (e *KnowledgeExposure) checkAnchor(ctx context.Context, guard *botruntime.ToolResultGuard) (valid bool, err error) {
	err = e.port.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { valid = knowledgeAnchorValid(guard, reads); return nil })
	return
}
