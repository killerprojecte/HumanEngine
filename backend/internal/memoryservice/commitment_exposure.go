package memoryservice

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/commitments"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// CommitmentExposure grants whole original state receipts, not ordinary memory
// parent authority. Current assertion proof qualification remains the owner.
type CommitmentExposure struct {
	states *StateExposure
	port   *commitmentExposurePort
}
type commitmentExposurePort struct{ memory *ExposurePort }

func NewCommitmentExposure(memory *ExposurePort) (*CommitmentExposure, error) {
	if memory == nil {
		return nil, errors.New("commitment_exposure_owner_required")
	}
	p := &commitmentExposurePort{memory}
	states, err := NewStateExposure(p, nil)
	if err != nil {
		return nil, err
	}
	return &CommitmentExposure{states, p}, nil
}
func (e *CommitmentExposure) Query(ctx context.Context, c *botruntime.TurnContext, input *Object) ([]*Object, error) {
	if input == nil {
		input = new(Object)
	}
	return e.states.Query(ctx, c, input)
}
func (e *CommitmentExposure) Deliver(ctx context.Context, c *botruntime.TurnContext, values []any) ([]*Object, error) {
	return e.states.Deliver(ctx, c, values)
}
func (e *CommitmentExposure) ReadDelivered(ctx context.Context, c *botruntime.TurnContext) ([]*Object, error) {
	return e.states.ReadDelivered(ctx, c)
}
func (e *CommitmentExposure) HasExposure(c *botruntime.TurnContext) bool {
	return e.states.HasExposure(c)
}
func (e *CommitmentExposure) CaptureGuard(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	return e.states.CaptureGuard(c)
}
func (e *CommitmentExposure) ReadExposedRefs(ctx context.Context, c *botruntime.TurnContext) ([]commitments.Exposure, error) {
	values, err := e.ReadDelivered(ctx, c)
	if err != nil {
		return nil, err
	}
	out := []commitments.Exposure{}
	seen := map[commitments.Exposure]bool{}
	for _, v := range values {
		view := object(value(v, "view"))
		ref := object(value(view, "ref"))
		r := commitments.Exposure{Reference: commitments.Reference{ID: text(ref, "id"), Version: commitmentNumber(ref, "version"), DeletionEpoch: commitmentNumber(ref, "deletionEpoch")}, MutationVersion: commitmentNumber(view, "mutationVersion")}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// Runtime settings are read from this supplied snapshot, including an active
// writer's uncommitted config. They are never cached at construction or query.
func (p *commitmentExposurePort) settings(scope sourceevents.RuntimeReadScope) (*Object, error) {
	rows, err := scope.Query("SELECT value FROM config WHERE key='settings'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return new(Object), rows.Err()
	}
	var raw string
	if err = rows.Scan(&raw); err != nil {
		return nil, err
	}
	v, err := jsjson.Parse(raw)
	if err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return object(v), nil
}
func (p *commitmentExposurePort) withCurrent(reads botruntime.ReadAuthority, c Context, fn func(*commitments.ReadService, commitments.Context) error) (allowed bool, err error) {
	return p.withRead(reads, c, false, fn)
}
func (p *commitmentExposurePort) withRead(reads botruntime.ReadAuthority, c Context, historical bool, fn func(*commitments.ReadService, commitments.Context) error) (allowed bool, err error) {
	if c.Simulation {
		return false, nil
	}
	err = p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		if err := scope.CheckBinding(p.memory.main, p.memory.authority); err != nil {
			return err
		}
		settings, err := p.settings(scope)
		if err != nil {
			return err
		}
		if !truthy(value(settings, "memoryCommitmentsEnabled")) || !truthy(value(settings, "memoryCommitmentsAnswerEnabled")) || historical && !truthy(value(settings, "memoryHistoricalReadEnabled")) {
			return nil
		}
		if err := scope.Ready(); err != nil {
			return err
		}
		allowed = true
		return p.memory.memory.d.Commitments.ReadRuntimeCurrent(scope, p.memory.memory.d.Assertions, func(c commitments.Context) (bool, error) {
			settings, err := p.settings(scope)
			return !c.Simulation && truthy(value(settings, "memoryCommitmentsEnabled")), err
		}, p.memory.memory.d.Authority, func(reader *commitments.ReadService) error {
			return fn(reader, commitments.Context{SceneID: c.SceneID, Simulation: c.Simulation})
		})
	})
	return
}
func (p *commitmentExposurePort) Query(ctx context.Context, c Context, input any) (out []*Object, err error) {
	out = []*Object{}
	err = p.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		_, err := p.withCurrent(reads, c, func(reader *commitments.ReadService, c commitments.Context) error {
			query := object(input)
			lifecycle := "open"
			var life *string = &lifecycle
			if query != nil {
				if v, present := query.Get("lifecycle"); present {
					if v == nil || jsjson.IsUndefined(v) {
						life = nil
					} else if s, ok := v.(string); ok {
						lifecycle = s
					} else {
						return errors.New("commitment_read_lifecycle")
					}
				}
			}
			limit := float64(12)
			page, err := reader.List(c, commitments.ReadOptions{PersonID: text(query, "personId"), Lifecycle: life, Limit: &limit})
			if err != nil {
				return err
			}
			items, ok := value(page, "items").([]any)
			if !ok || text(page, "coverage") != "complete" || len(items) > 12 {
				return errors.New("commitment_query_limited")
			}
			for _, view := range items {
				out = append(out, epObj("stateKind", "commitment", "view", view))
			}
			return nil
		})
		return err
	})
	return
}
func (p *commitmentExposurePort) Valid(ctx context.Context, c Context, values []*Object) (valid bool, err error) {
	err = p.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		var err error
		valid, err = p.validate(reads, c, values)
		return err
	})
	return
}
func (p *commitmentExposurePort) ValidScoped(reads botruntime.ReadAuthority, c Context, values []*Object) (bool, error) {
	valid, err := p.validate(reads, c, values)
	// Expired or foreign native capabilities confer no authority. Other owner
	// failures still propagate, matching the source valid port without a catch.
	if errors.Is(err, sql.ErrTxDone) || err != nil && err.Error() == "runtime_read_scope_foreign" {
		return false, nil
	}
	return valid, err
}
func (p *commitmentExposurePort) validate(reads botruntime.ReadAuthority, c Context, values []*Object) (bool, error) {
	valid := true
	allowed, err := p.withCurrent(reads, c, func(reader *commitments.ReadService, c commitments.Context) error {
		for _, v := range values {
			view := object(value(v, "view"))
			ref := object(value(view, "ref"))
			if text(v, "stateKind") != "commitment" || text(view, "sceneId") != c.SceneID {
				valid = false
				return nil
			}
			current, err := reader.Current(c, commitments.Reference{ID: text(ref, "id"), Version: commitmentNumber(ref, "version"), DeletionEpoch: commitmentNumber(ref, "deletionEpoch")})
			if err != nil {
				return err
			}
			if !deepEqual(current, view) {
				valid = false
				return nil
			}
		}
		return nil
	})
	return err == nil && allowed && valid, err
}
func commitmentViewSources(view *Object) []string {
	evidence, _ := value(view, "evidence").([]any)
	all := append([]any{}, evidence...)
	groups, _ := value(view, "requiredProposalGroups").([]any)
	for _, g := range groups {
		members, _ := g.([]any)
		all = append(all, members...)
	}
	ids := []string{}
	for _, raw := range all {
		proofs, _ := value(object(raw), "proofs").([]any)
		for _, raw := range proofs {
			sources, _ := value(object(raw), "sources").([]any)
			for _, raw := range sources {
				ids = append(ids, text(object(raw), "messageId"))
			}
		}
	}
	return ids
}
func (p *commitmentExposurePort) SourceIDs(values []*Object) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		for _, id := range commitmentViewSources(object(value(v, "view"))) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out, nil
}
func (e *CommitmentExposure) ComposeExtension(base botruntime.Extension) *botruntime.Extension {
	ext := base
	ext.CaptureToolResultGuards = func(c *botruntime.TurnContext) []*botruntime.ToolResultGuard {
		out := []*botruntime.ToolResultGuard{}
		if base.CaptureToolResultGuards != nil {
			out = append(out, base.CaptureToolResultGuards(c)...)
		}
		if guard := e.CaptureGuard(c); guard != nil {
			out = append(out, guard)
		}
		return out
	}
	ext.Delivered = func(name string, v any, c *botruntime.TurnContext, input any) (any, error) {
		if name == "memory.commitments" {
			var selected []any
			switch values := v.(type) {
			case []any:
				selected = values
			case []*Object:
				selected = make([]any, len(values))
				for i, b := range values {
					selected[i] = b
				}
			}
			if selected != nil {
				out, err := e.Deliver(context.Background(), c, selected)
				if err != nil {
					return nil, err
				}
				values := make([]any, len(out))
				for i, b := range out {
					values[i] = b
				}
				v = values
			}
		}
		if base.Delivered != nil {
			return base.Delivered(name, v, c, input)
		}
		return v, nil
	}
	return &ext
}

func commitmentNumber(o *Object, key string) float64 { n, _ := value(o, key).(float64); return n }
