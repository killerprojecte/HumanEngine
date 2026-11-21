package app

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func (a *Application) experienceOutcomesExtension(settings *settingsSnapshot) *botruntime.Extension {
	outcomes := []*jsjson.Object{memoryToolObject(
		"name", "experience.outcomes",
		"description", "Read bounded source-linked explicit feedback observations for experimental operation policies. Instructions are preferences, not evidence of success or truth.",
		"inputSchema", memoryToolObject("$schema", "https://json-schema.org/draft/2020-12/schema", "type", "object", "properties", new(jsjson.Object), "additionalProperties", false),
		"effect", "read",
	)}
	return &botruntime.Extension{
		ToolsForTurn: func(turn *botruntime.TurnContext) []*jsjson.Object {
			// Keep the advertised dispatch owner through delivery. Outcomes and
			// DeliverOutcomes each read the current durable feature setting.
			return turn.OwnedExtensionState(outcomes[0], func() any {
				if settings.enabled("memoryExperienceEnabled") {
					return outcomes
				}
				return []*jsjson.Object(nil)
			}).([]*jsjson.Object)
		},
		Execute: func(ctx context.Context, _ string, _ any, turn *botruntime.TurnContext, done func(any, error)) {
			if err := a.Engine.TurnRead.GateTool(ctx, turn); err != nil {
				done(nil, err)
				return
			}
			value, err := a.ExperienceHints.Outcomes(turn)
			done(value, err)
		},
		Delivered: func(_ string, value any, turn *botruntime.TurnContext, _ any) (any, error) {
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
