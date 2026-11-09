package app

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
)

// experiencePlanner preserves Engine.plannerContextData ordering. A temporary
// ticket fences the actual read, but the turn acquires persistent model-read
// authority only when the service supplies a nonempty planner context.
type experiencePlannerPolicy interface {
	PersonalizationAllowed(context.Context, string) (bool, error)
	Capture(context.Context, string) (*modelread.Ticket, error)
	Assert(context.Context, *modelread.Ticket) error
	Begin(context.Context, *botruntime.TurnContext) (*modelread.Ticket, error)
}

func (a *Application) experiencePlanner(c *botruntime.TurnContext, rows []*jsjson.Object) (any, error) {
	return runExperiencePlanner(context.Background(), c, a.Engine.TurnRead, func() (any, error) { return a.ExperienceHints.PlannerContext(c, rows) })
}

func runExperiencePlanner(ctx context.Context, c *botruntime.TurnContext, read experiencePlannerPolicy, planner func() (any, error)) (any, error) {
	if c.Mode != "real" {
		return nil, nil
	}
	allowed, err := read.PersonalizationAllowed(ctx, c.Scene.ID)
	if err != nil || !allowed {
		return nil, err
	}
	ticket, err := read.Capture(ctx, c.Scene.ID)
	if err != nil {
		return nil, err
	}
	value, err := planner()
	if err != nil {
		return nil, err
	}
	if err = read.Assert(ctx, ticket); err != nil {
		return nil, err
	}
	if value != nil {
		if _, err = read.Begin(ctx, c); err != nil {
			return nil, err
		}
	}
	return value, nil
}
