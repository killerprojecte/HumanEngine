package app

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

const (
	episodeTool    = "memory.episodes"
	workingSetTool = "memory.workingSet"
)

func (a *Application) registerWorkspaceCapture() {
	a.Engine.Runtime.AddExtension(&botruntime.Extension{CaptureToolResultGuards: func(c *botruntime.TurnContext) []*botruntime.ToolResultGuard {
		var guards []*botruntime.ToolResultGuard
		for _, exposure := range []*memoryservice.StateExposure{a.WorkingSetExposure, a.EpisodeExposure} {
			if guard := exposure.CaptureGuard(c); guard != nil {
				guards = append(guards, guard)
			}
		}
		return guards
	}})
}

func (a *Application) registerWorkspaceTools(snapshot *settingsSnapshot) {
	a.registerWorkspaceTool(snapshot, workingSetTool, "memoryWorkingSetEnabled",
		"Read a bounded optional working set of currently authorized assertion and declared plan entries. Hot, cold, pinned and context_selected are accessibility metadata, never truth, success or evidence of absence. Select the entire qualified page via stateEvidenceIds; limited coverage means other records may exist. includeCold explicitly allows cold candidates. Reading this state does not authorize new derived learning.",
		a.WorkingSetExposure, true)
	a.registerWorkspaceTool(snapshot, episodeTool, "memoryEpisodesEnabled",
		"Read independent qualified experience records and shared-plan self-declarations. These are not synthesized or observed joint events; do not infer causes, emotions, participation or unknown occurrence times. Select the whole page via stateEvidenceIds and preserve coverage. Reading it does not authorize new derived learning.",
		a.EpisodeExposure, false)
}

func (a *Application) registerWorkspaceTool(snapshot *settingsSnapshot, name, flag, description string, exposure *memoryservice.StateExposure, workingSet bool) {
	properties := memoryToolObject("personId", memoryToolObject("type", "string"))
	if workingSet {
		_ = properties.Set("includeCold", memoryToolObject("type", "boolean"))
	}
	tools := []*jsjson.Object{memoryToolObject("name", name, "description", description, "inputSchema", memoryToolObject("$schema", "https://json-schema.org/draft/2020-12/schema", "type", "object", "properties", properties, "additionalProperties", false))}
	argumentError := errors.New("memory_episodes_arguments_invalid")
	if workingSet {
		argumentError = errors.New("memory_working_set_arguments_invalid")
	}
	a.Engine.Runtime.AddExtension(&botruntime.Extension{
		ToolsForTurn: func(c *botruntime.TurnContext) []*jsjson.Object {
			// Retain the advertised delivery owner after a live feature change.
			return c.OwnedExtensionState(tools[0], func() any {
				if snapshot.enabled(flag) {
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
			query, ok := workspaceToolQuery(input, workingSet)
			if !ok {
				done(nil, argumentError)
				return
			}
			out, err := exposure.Query(ctx, c, query)
			if err == nil && !c.MemoryExposureDeferred() {
				out, err = exposure.Deliver(ctx, c, memoryToolArray(out))
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
			out, err := exposure.Deliver(ctx, c, values)
			return memoryToolArray(out), err
		},
	})
}

func workspaceToolQuery(input any, workingSet bool) (any, bool) {
	args, ok := input.(*jsjson.Object)
	if !ok || args == nil {
		return nil, false
	}
	var q memoryservice.WorkingSetToolQuery
	for _, member := range args.Members() {
		switch member.Key {
		case "personId":
			q.PersonID, ok = member.Value.(string)
		case "includeCold":
			if !workingSet {
				return nil, false
			}
			q.IncludeCold, ok = member.Value.(bool)
		default:
			return nil, false
		}
		if !ok {
			return nil, false
		}
	}
	if workingSet {
		return q, true
	}
	return q.PersonID, true
}
