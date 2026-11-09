package memoryservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/relations"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"runtime"
	"sync"
	"time"
	"weak"
)

type relationPending struct {
	prepared, body *Object
	edges          []*Object
	person         string
}
type relationTurn struct {
	operation   sync.Mutex
	pending     map[weak.Pointer[Object]]*relationPending
	delivered   []*relationPending
	entities    map[string]*Object
	entityOrder []string
	checks      map[string][]*relationPending
}
type relationCleanup struct {
	state weak.Pointer[relationTurn]
	key   weak.Pointer[Object]
}

func cleanupRelation(p relationCleanup) {
	if s := p.state.Value(); s != nil {
		s.operation.Lock()
		delete(s.pending, p.key)
		s.operation.Unlock()
	}
}

type RelationExposure struct {
	port     *ExposurePort
	memories *ModelMemoryExposure
	now      func() float64
}

func NewRelationExposure(port *ExposurePort, memories *ModelMemoryExposure, now func() float64) (*RelationExposure, error) {
	if port == nil || memories == nil {
		return nil, errors.New("relation_exposure_owners_required")
	}
	if now == nil {
		now = func() float64 { return float64(time.Now().UnixMilli()) }
	}
	return &RelationExposure{port, memories, now}, nil
}
func (e *RelationExposure) turn(c *botruntime.TurnContext) *relationTurn {
	return c.OwnedExtensionState(e, func() any {
		return &relationTurn{pending: map[weak.Pointer[Object]]*relationPending{}, entities: map[string]*Object{}, checks: map[string][]*relationPending{}}
	}).(*relationTurn)
}
func (e *RelationExposure) scope(reads botruntime.ReadAuthority, c Context, fn func(*assertions.Reader, relations.Options) error) (allowed bool, err error) {
	if c.Simulation {
		return false, nil
	}
	err = e.port.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		if err := scope.CheckBinding(e.port.main, e.port.authority); err != nil {
			return err
		}
		settings, err := (&commitmentExposurePort{e.port}).settings(scope)
		if err != nil {
			return err
		}
		if !truthy(value(settings, "memoryGraphEnabled")) || !truthy(value(settings, "memoryGraphAnswerEnabled")) {
			return nil
		}
		allowed = true
		return e.port.withScopeReader(scope, func(r *assertions.Reader) error {
			return fn(r, relations.Options{Authority: e.port.memory.d.Authority, Enabled: func(relations.Context) (bool, error) { return truthy(value(settings, "memoryGraphEnabled")), nil }})
		})
	})
	return
}
func (e *RelationExposure) list(reads botruntime.ReadAuthority, c Context, person string) (out relations.Listing, allowed bool, err error) {
	out = relations.Listing{Items: []*Object{}, Coverage: "complete"}
	allowed, err = e.scope(reads, c, func(r *assertions.Reader, options relations.Options) error {
		var er error
		limit := float64(40)
		out, er = relations.ListInReadScope(r, options, relations.Context{SceneID: c.SceneID, Simulation: c.Simulation, Provenance: "contextual"}, relations.ListOptions{PersonID: person, Limit: &limit})
		return er
	})
	return
}
func relationObject(values ...any) *Object {
	o := new(Object)
	for i := 0; i < len(values); i += 2 {
		o.Set(values[i].(string), values[i+1])
	}
	return o
}
func (e *RelationExposure) applicability(edge *Object) (string, error) {
	temporal := text(edge, "temporalScope")
	if temporal == "past" || temporal == "future" {
		return temporal, nil
	}
	if s := text(edge, "validFrom"); s != "" {
		n, valid, err := e.port.memory.d.Authority.DateParser.ParseMillis(s)
		if err != nil {
			return "", err
		}
		if valid && float64(n) > e.now() {
			return "future", nil
		}
	}
	if s := text(edge, "validUntil"); s != "" {
		n, valid, err := e.port.memory.d.Authority.DateParser.ParseMillis(s)
		if err != nil {
			return "", err
		}
		if valid && float64(n) <= e.now() {
			return "expired", nil
		}
	}
	if temporal == "current" {
		return "current", nil
	}
	return "unspecified", nil
}
func relationIDs(edge *Object) []string {
	out := []string{text(object(value(edge, "backing")), "id")}
	target := text(object(value(object(value(edge, "target")), "backing")), "id")
	if target != out[0] {
		out = append(out, target)
	}
	return out
}
func (e *RelationExposure) Query(ctx context.Context, c *botruntime.TurnContext, person string) ([]*Object, error) {
	empty := []*Object{}
	if exposureAborted(c) || c.Mode == "simulation" {
		return empty, nil
	}
	var listed relations.Listing
	var allowed bool
	err := e.port.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		var er error
		listed, allowed, er = e.list(reads, exposureContext(c), person)
		return er
	})
	if err != nil {
		return nil, err
	}
	if !allowed {
		return empty, nil
	}
	if listed.Coverage != "complete" {
		return nil, errors.New("relation_query_limited")
	}
	edges := []*Object{}
	ids := []string{}
	seen := map[string]bool{}
	for _, edge := range listed.Items {
		if text(edge, "status") == "rejected" {
			continue
		}
		edges = append(edges, edge)
		for _, id := range relationIDs(edge) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) > 12 {
		return nil, errors.New("relation_query_limited")
	}
	records, err := e.port.memory.List(ctx, exposureContext(c), "")
	if err != nil {
		return nil, err
	}
	byID := map[string]*Object{}
	for _, r := range records {
		byID[text(r, "id")] = r
	}
	selected := []*Object{}
	for _, id := range ids {
		if byID[id] == nil {
			return nil, errors.New("relation_snapshot_unavailable")
		}
		selected = append(selected, byID[id])
	}
	prepared, err := e.memories.Prepare(ctx, c, selected)
	if err != nil {
		return nil, err
	}
	if len(prepared) != len(ids) {
		return nil, errors.New("relation_snapshot_unavailable")
	}
	s := e.turn(c)
	s.operation.Lock()
	defer s.operation.Unlock()
	out := []*Object{}
	for _, record := range prepared {
		linked := []*Object{}
		evidence := []any{}
		for _, edge := range edges {
			matches := false
			for _, id := range relationIDs(edge) {
				matches = matches || id == text(record, "id")
			}
			if !matches {
				continue
			}
			linked = append(linked, exposureClone(edge).(*Object))
			ev := new(Object)
			for _, key := range []string{"id", "version", "predicate", "modality", "temporalScope", "personId", "polarity", "conditions", "status", "admission"} {
				ev.Set(key, exposureClone(value(edge, key)))
			}
			target := object(value(edge, "target"))
			ev.Set("target", relationObject("entityId", value(target, "id"), "kind", value(target, "kind"), "title", value(target, "title")))
			for _, key := range []string{"occurredAt", "validFrom", "validUntil", "epistemicKind"} {
				if truthy(value(edge, key)) {
					ev.Set(key, value(edge, key))
				}
			}
			applicable, err := e.applicability(edge)
			if err != nil {
				return nil, err
			}
			ev.Set("applicability", applicable)
			required := []any{}
			for _, id := range relationIDs(edge) {
				required = append(required, id)
			}
			ev.Set("requiredMemoryIds", required)
			sources := []any{}
			proofs, _ := value(edge, "proofs").([]any)
			for _, proof := range proofs {
				rows, _ := value(object(proof), "sources").([]any)
				for _, row := range rows {
					sources = append(sources, exposureClone(row))
				}
			}
			ev.Set("sources", sources)
			evidence = append(evidence, ev)
		}
		body := exposureClone(record).(*Object)
		body.Set("relationEvidence", evidence)
		body.Set("relationCoverage", "complete")
		key := weak.Make(body)
		s.pending[key] = &relationPending{record, exposureClone(body).(*Object), linked, person}
		runtime.AddCleanup(body, cleanupRelation, relationCleanup{weak.Make(s), key})
		out = append(out, body)
	}
	return out, nil
}
func (e *RelationExposure) valid(reads botruntime.ReadAuthority, c Context, selected []*relationPending) (bool, error) {
	people := []string{}
	seen := map[string]bool{}
	for _, p := range selected {
		if !seen[p.person] {
			seen[p.person] = true
			people = append(people, p.person)
		}
	}
	current := map[string]*Object{}
	for _, person := range people {
		listing, allowed, err := e.list(reads, c, person)
		if err != nil {
			return false, err
		}
		if !allowed || listing.Coverage != "complete" {
			return false, nil
		}
		for _, edge := range listing.Items {
			current[text(edge, "id")] = edge
		}
	}
	targets, captured := map[string]bool{}, map[string]bool{}
	for _, p := range selected {
		for _, edge := range p.edges {
			targets[text(object(value(edge, "target")), "id")] = true
			captured[text(edge, "id")] = true
		}
	}
	for id, edge := range current {
		if text(edge, "status") != "rejected" && targets[text(object(value(edge, "target")), "id")] && !captured[id] {
			return false, nil
		}
	}
	for _, p := range selected {
		for _, edge := range p.edges {
			if !deepEqual(current[text(edge, "id")], edge) {
				return false, nil
			}
			evidence, _ := value(p.body, "relationEvidence").([]any)
			found := false
			for _, ev := range evidence {
				if text(object(ev), "id") == text(edge, "id") {
					applicable, err := e.applicability(edge)
					if err != nil {
						return false, err
					}
					found = text(object(ev), "applicability") == applicable
					break
				}
			}
			if !found {
				return false, nil
			}
		}
	}
	return true, nil
}
func (e *RelationExposure) withValid(ctx context.Context, c Context, selected []*relationPending) (valid bool, err error) {
	err = e.port.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		var er error
		valid, er = e.valid(reads, c, selected)
		return er
	})
	return
}
func copyRelationPending(values []*relationPending) []*relationPending {
	out := make([]*relationPending, len(values))
	for i, p := range values {
		out[i] = &relationPending{body: exposureClone(p.body).(*Object), person: p.person}
		for _, edge := range p.edges {
			out[i].edges = append(out[i].edges, exposureClone(edge).(*Object))
		}
	}
	return out
}
func (e *RelationExposure) Deliver(ctx context.Context, c *botruntime.TurnContext, values []any) ([]*Object, error) {
	empty := []*Object{}
	if exposureAborted(c) || c.Mode == "simulation" {
		return empty, nil
	}
	s := e.turn(c)
	s.operation.Lock()
	defer s.operation.Unlock()
	selected := []*relationPending{}
	bodies := []*Object{}
	ids := map[string]bool{}
	for _, v := range values {
		body, _ := v.(*Object)
		if body == nil {
			return empty, nil
		}
		p := s.pending[weak.Make(body)]
		if p == nil || !deepEqual(body, p.body) {
			return empty, nil
		}
		selected = append(selected, p)
		bodies = append(bodies, body)
		ids[text(body, "id")] = true
	}
	if len(ids) != len(selected) {
		return empty, nil
	}
	for _, p := range selected {
		for _, edge := range p.edges {
			for _, id := range relationIDs(edge) {
				if !ids[id] {
					return empty, nil
				}
			}
		}
	}
	valid, err := e.withValid(ctx, exposureContext(c), selected)
	if err != nil || !valid {
		return empty, err
	}
	for _, p := range selected {
		for _, edge := range p.edges {
			target := object(value(edge, "target"))
			id := text(target, "id")
			if previous := s.entities[id]; previous != nil {
				ok := deepEqual(previous, value(target, "backing"))
				if ok {
					ok, err = e.withValid(ctx, exposureContext(c), s.checks[id])
					if err != nil {
						return nil, err
					}
				}
				if !ok {
					rejectExposure(c)
					return empty, nil
				}
			}
		}
	}
	prepared := []any{}
	for _, p := range selected {
		prepared = append(prepared, p.prepared)
	}
	committed, err := e.memories.Commit(ctx, c, prepared)
	if err != nil {
		return nil, err
	}
	if len(committed) != len(selected) {
		return empty, nil
	}
	for _, p := range selected {
		for _, edge := range p.edges {
			target := object(value(edge, "target"))
			id := text(target, "id")
			if s.entities[id] == nil {
				s.entities[id] = exposureClone(value(target, "backing")).(*Object)
				s.entityOrder = append(s.entityOrder, id)
				s.checks[id] = copyRelationPending(selected)
			}
		}
	}
	s.delivered = append(s.delivered, copyRelationPending(selected)...)
	scope := exposureContext(c)
	captured := copyRelationPending(selected)
	c.AddScopedValidation(func(reads botruntime.ReadAuthority) (bool, error) {
		if exposureAborted(c) {
			return false, nil
		}
		return e.valid(reads, scope, captured)
	})
	return bodies, nil
}
func (e *RelationExposure) CaptureLearningGuard(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	s := e.turn(c)
	s.operation.Lock()
	defer s.operation.Unlock()
	if len(s.delivered) == 0 || len(s.entities) == 0 {
		return nil
	}
	captured := copyRelationPending(s.delivered)
	scope := exposureContext(c)
	return &botruntime.ToolResultGuard{Check: func(reads botruntime.ReadAuthority) (bool, error) { return e.valid(reads, scope, captured) }}
}
func (e *RelationExposure) ReadExposedEntities(ctx context.Context, c *botruntime.TurnContext) ([]*Object, error) {
	out := []*Object{}
	if exposureAborted(c) {
		return out, nil
	}
	s := e.turn(c)
	s.operation.Lock()
	defer s.operation.Unlock()
	for _, id := range s.entityOrder {
		ok, err := e.withValid(ctx, exposureContext(c), s.checks[id])
		if err != nil || !ok {
			return out, err
		}
	}
	for _, id := range s.entityOrder {
		out = append(out, relationObject("entityId", id, "backingRef", exposureClone(s.entities[id])))
	}
	return out, nil
}
