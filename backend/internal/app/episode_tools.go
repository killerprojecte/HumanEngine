package app

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

const episodeTool = "memory.episodes"

func (a *Application) registerEpisodeCapture() {
	a.Engine.Runtime.AddExtension(&botruntime.Extension{CaptureToolResultGuards: func(c *botruntime.TurnContext) []*botruntime.ToolResultGuard {
		if guard := a.EpisodeExposure.CaptureGuard(c); guard != nil {
			return []*botruntime.ToolResultGuard{guard}
		}
		return nil
	}})
}

func (a *Application) registerEpisodeTool(snapshot *settingsSnapshot) {
	tools := []*jsjson.Object{memoryToolObject("name", episodeTool, "description", "Read independent qualified experience records and shared-plan self-declarations. These are not synthesized or observed joint events; do not infer causes, emotions, participation or unknown occurrence times. Select the whole page via stateEvidenceIds and preserve coverage. Reading it does not authorize new derived learning.", "inputSchema", memoryToolObject("$schema", "https://json-schema.org/draft/2020-12/schema", "type", "object", "properties", memoryToolObject("personId", memoryToolObject("type", "string")), "additionalProperties", false))}
	a.Engine.Runtime.AddExtension(&botruntime.Extension{
		ToolsForTurn: func(c *botruntime.TurnContext) []*jsjson.Object {
			// Retain the advertised delivery owner after a live feature change.
			return c.OwnedExtensionState(tools[0], func() any {
				if snapshot.enabled("memoryEpisodesEnabled") {
					return tools
				}
				return []*jsjson.Object(nil)
			}).([]*jsjson.Object)
		},
		Execute: func(ctx context.Context, _ string, input any, c *botruntime.TurnContext, done func(any, error)) {
			if err := a.Engine.TurnRead.GateTool(ctx, c); err != nil {
				done(nil, err)
				return
			}
			args, ok := input.(*jsjson.Object)
			if !ok || args == nil {
				done(nil, errors.New("memory_episodes_arguments_invalid"))
				return
			}
			var person string
			if raw, has := args.Get("personId"); has {
				person, ok = raw.(string)
				if !ok || args.Len() != 1 {
					done(nil, errors.New("memory_episodes_arguments_invalid"))
					return
				}
			} else if args.Len() != 0 {
				done(nil, errors.New("memory_episodes_arguments_invalid"))
				return
			}
			out, err := a.EpisodeExposure.Query(ctx, c, person)
			if err == nil && !c.MemoryExposureDeferred() {
				out, err = a.EpisodeExposure.Deliver(ctx, c, memoryToolArray(out))
			}
			done(memoryToolArray(out), err)
		},
		Delivered: func(_ string, v any, c *botruntime.TurnContext, _ any) (any, error) {
			ctx := context.Background()
			if err := a.Engine.TurnRead.GateTool(ctx, c); err != nil {
				return nil, err
			}
			values, ok := v.([]any)
			if !ok {
				return v, nil
			}
			out, err := a.EpisodeExposure.Deliver(ctx, c, values)
			return memoryToolArray(out), err
		},
	})
}
