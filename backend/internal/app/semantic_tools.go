package app

import (
	"context"
	_ "embed"
	"errors"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

//go:embed testdata/semantic-tool-source.json
var semanticToolSource string

func semanticToolDefinition() *jsjson.Object {
	v, e := jsjson.Parse(semanticToolSource)
	if e != nil {
		panic(e)
	}
	return v.(*jsjson.Object)
}
func (a *Application) composeSemanticMemory(o Owners, memory *memoryOwners) error {
	port, e := memoryservice.NewExposurePort(a.Semantic.Memory, o.Sources, o.Main, o.Authority)
	if e != nil {
		return e
	}
	a.SemanticExposure, e = memoryservice.NewSemanticExposure(port, a.Engine.TurnRead, func() int64 { return time.Now().UnixMilli() })
	if e != nil {
		return e
	}
	a.Engine.Runtime.AddExtension(&botruntime.Extension{
		ToolsForTurn: func(*botruntime.TurnContext) []*jsjson.Object {
			if memory.snapshot.enabled("memorySemanticEnabled") && memory.snapshot.enabled("memorySemanticAnswerEnabled") {
				return []*jsjson.Object{semanticToolDefinition()}
			}
			return nil
		},
		Execute: func(ctx context.Context, name string, input any, c *botruntime.TurnContext, done func(any, error)) {
			if name != "memory.summaries" {
				done(nil, errors.New("runtime_unknown_tool"))
				return
			}
			if c.Mode == "real" {
				if _, err := a.Engine.TurnRead.Begin(ctx, c); err != nil {
					done(nil, err)
					return
				}
			}
			args, ok := input.(*jsjson.Object)
			if !ok || args == nil || args.Len() != 0 {
				done(nil, errors.New("semantic_tool_input_invalid"))
				return
			}
			values, err := a.SemanticExposure.Query(ctx, c)
			if err == nil && !c.MemoryExposureDeferred() {
				values, err = a.SemanticExposure.Deliver(ctx, c, memoryToolArray(values))
			}
			done(memoryToolArray(values), err)
		},
		Delivered: func(name string, v any, c *botruntime.TurnContext, input any) (any, error) {
			if name != "memory.summaries" {
				return v, nil
			}
			if c.Mode == "real" {
				if _, err := a.Engine.TurnRead.Begin(context.Background(), c); err != nil {
					return nil, err
				}
			}
			values, ok := v.([]any)
			if !ok {
				return []any{}, nil
			}
			out, err := a.SemanticExposure.Deliver(context.Background(), c, values)
			if err != nil {
				return nil, err
			}
			result := memoryToolArray(out)
			if err = a.ExperienceHints.DeliveredTool(c, name, input, result); err != nil {
				return nil, err
			}
			return result, nil
		},
		CaptureToolResultGuards: func(c *botruntime.TurnContext) []*botruntime.ToolResultGuard {
			if guard := a.SemanticExposure.CaptureGuard(c); guard != nil {
				return []*botruntime.ToolResultGuard{guard}
			}
			return nil
		},
	})
	return nil
}
