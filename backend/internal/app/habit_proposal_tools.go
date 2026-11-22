package app

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

const habitProposalTool = "habits.propose"

func (a *Application) composeHabitProposal(o Owners) error {
	schema, err := a.Learning.Runner.HabitProposalSchema()
	if err != nil {
		return err
	}
	a.Engine.Runtime.AddExtension(&botruntime.Extension{
		Tools: []*jsjson.Object{memoryToolObject("name", habitProposalTool, "description",
			"Propose a low-weight scene habit or structured bot expression preference from explicit live feedback, never silence or bot usage. No personal facts in shared preferences.", "inputSchema", schema)},
		Execute: func(ctx context.Context, _ string, input any, c *botruntime.TurnContext, done func(any, error)) {
			value, err := a.habitProposal(ctx, o, input, c)
			done(value, err)
		},
	})
	return nil
}

func (a *Application) habitProposal(ctx context.Context, o Owners, input any, c *botruntime.TurnContext) (*jsjson.Object, error) {
	reject := func(reason string) (*jsjson.Object, error) {
		return memoryToolObject("status", "rejected", "reason", reason), nil
	}
	// State reads cannot authorize new free-form habits. This is the source
	// ordering; ordinary message context does not require a model-read ticket.
	if a.SemanticExposure.CaptureGuard(c) != nil {
		return reject("semantic_read_not_learning_authority")
	}
	if a.IdentityExposure.CaptureGuard(c) != nil {
		return reject("identity_read_not_learning_authority")
	}
	if a.KnowledgeExposure.CaptureGuard(c) != nil {
		return reject("knowledge_state_not_freeform_learning_authority")
	}
	data, _ := input.(*jsjson.Object)
	candidate, err := a.Learning.Runner.ParseHabitProposal(data)
	if err != nil {
		return nil, errors.New("habit_proposal_arguments_invalid")
	}
	if c.Mode == "simulation" {
		return reject("simulation_does_not_learn")
	}
	raw, _ := candidate.Get("sourceMessageIds")
	sources := raw.([]any)
	ids := make([]string, len(sources))
	for i, v := range sources {
		ids[i] = v.(string)
	}
	candidate.Delete("sourceMessageIds")
	messages, captured, err := a.habitActionSnapshots(c)
	if err != nil {
		return nil, err
	}
	return o.Habits.ProposeFromSnapshots(ctx, c.Scene.ID, candidate, ids, messages, captured, o.Sources, o.Dates)
}
