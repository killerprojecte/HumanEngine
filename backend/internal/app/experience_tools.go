package app

import (
	"context"
	_ "embed"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

//go:embed testdata/experience-proposal-tool-source.json
var experienceProposalToolSource string

func (a *Application) experienceToolsExtension(settings *settingsSnapshot) *botruntime.Extension {
	outcomes := memoryToolObject(
		"name", "experience.outcomes",
		"description", "Read bounded source-linked explicit feedback observations for experimental operation policies. Instructions are preferences, not evidence of success or truth.",
		"inputSchema", memoryToolObject("$schema", "https://json-schema.org/draft/2020-12/schema", "type", "object", "properties", new(jsjson.Object), "additionalProperties", false),
		"effect", "read",
	)
	propose, err := jsjson.Parse(experienceProposalToolSource)
	if err != nil {
		panic(err)
	}
	tools := []*jsjson.Object{outcomes, propose.(*jsjson.Object)}
	return &botruntime.Extension{
		ToolsForTurn: func(turn *botruntime.TurnContext) []*jsjson.Object {
			enabled := settings.enabled("memoryExperienceEnabled")
			if !turn.OwnedExtensionState(outcomes, func() any { return enabled }).(bool) {
				return nil
			}
			// Keep the read owner through delivery, but do not keep advertising
			// a write after the feature is disabled. The writer checks durable
			// flags under the same transaction as the outcome and candidate.
			if !enabled {
				return tools[:1]
			}
			return tools
		},
		Execute: func(ctx context.Context, name string, input any, turn *botruntime.TurnContext, done func(any, error)) {
			if err := a.Engine.TurnRead.GateTool(ctx, turn); err != nil {
				done(nil, err)
				return
			}
			if name == "experience.outcomes" {
				value, err := a.ExperienceHints.Outcomes(turn)
				done(value, err)
				return
			}
			if reason := a.freeFormLearningRejection(turn); reason != "" {
				done(memoryToolObject("status", "rejected", "reason", reason), nil)
				return
			}
			value, err := a.ExperienceHints.Propose(turn, input)
			done(value, err)
		},
		Delivered: func(name string, value any, turn *botruntime.TurnContext, _ any) (any, error) {
			if name != "experience.outcomes" {
				return value, nil
			}
			if err := a.Engine.TurnRead.GateTool(context.Background(), turn); err != nil {
				return nil, err
			}
			if rows, ok := value.([]any); ok {
				return a.ExperienceHints.DeliverOutcomes(turn, rows)
			}
			return value, nil
		},
	}
}
