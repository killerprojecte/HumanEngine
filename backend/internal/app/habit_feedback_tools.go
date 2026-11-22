package app

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const habitFeedbackTool = "habits.feedback"

func (a *Application) composeHabitFeedback(o Owners) {
	stringType := memoryToolObject("type", "string")
	schema := memoryToolObject("$schema", "https://json-schema.org/draft/2020-12/schema", "type", "object", "properties", memoryToolObject(
		"id", stringType,
		"direction", memoryToolObject("anyOf", []any{
			memoryToolObject("type", "number", "const", float64(1)),
			memoryToolObject("type", "number", "const", float64(-1)),
		}),
		"sourceMessageIds", memoryToolObject("minItems", float64(1), "maxItems", float64(20), "type", "array", "items", stringType),
	), "required", []any{"id", "direction", "sourceMessageIds"}, "additionalProperties", false)
	a.Engine.Runtime.AddExtension(&botruntime.Extension{
		Tools: []*jsjson.Object{memoryToolObject("name", habitFeedbackTool, "description",
			"Strengthen or weaken a known hint using explicit nonbot live feedback, never silence.", "inputSchema", schema)},
		Execute: func(ctx context.Context, name string, input any, c *botruntime.TurnContext, done func(any, error)) {
			if name != habitFeedbackTool {
				done(nil, errors.New("runtime_unknown_tool"))
				return
			}
			if c.Mode == "simulation" {
				done(memoryToolObject("status", "rejected", "reason", "simulation_does_not_learn"), nil)
				return
			}
			value, err := a.habitFeedback(ctx, o, input, c)
			done(value, err)
		},
	})
}

func (a *Application) habitFeedback(ctx context.Context, o Owners, input any, c *botruntime.TurnContext) (*jsjson.Object, error) {
	ticket, err := a.Engine.TurnRead.Begin(ctx, c)
	if err != nil {
		return nil, err
	}
	if !ticket.PersonalizationEnabled() {
		return nil, errors.New("personalization_disabled")
	}
	data, ok := input.(*jsjson.Object)
	if !ok || data.Len() != 3 {
		return nil, errors.New("habit_feedback_arguments_invalid")
	}
	rawID, _ := data.Get("id")
	rawDir, _ := data.Get("direction")
	rawSources, _ := data.Get("sourceMessageIds")
	id, idOK := rawID.(string)
	dir, dirOK := rawDir.(float64)
	sources, srcOK := rawSources.([]any)
	if !idOK || !dirOK || dir != 1 && dir != -1 || !srcOK || len(sources) == 0 || len(sources) > 20 {
		return nil, errors.New("habit_feedback_arguments_invalid")
	}
	ids := make([]string, len(sources))
	for i, value := range sources {
		source, ok := value.(string)
		if !ok {
			return nil, errors.New("habit_feedback_arguments_invalid")
		}
		ids[i] = source
	}
	messages, captured, err := a.habitActionSnapshots(c)
	if err != nil {
		return nil, err
	}
	return o.Habits.FeedbackFromSnapshots(ctx, c.Scene.ID, id, dir, ids, messages, captured, o.Sources, o.Dates, func(ws *recovery.WriteSession) bool {
		return a.Engine.TurnRead.ValidInSession(ctx, ticket, ws)
	})
}

func (a *Application) habitActionSnapshots(c *botruntime.TurnContext) ([]*jsjson.Object, bool, error) {
	if c.HasSourceSnapshots() {
		return c.Sources(), true, nil
	}
	snapshot, err := a.Engine.Runtime.Snapshot(c.Scene.ID)
	return snapshot.Messages, false, err
}
