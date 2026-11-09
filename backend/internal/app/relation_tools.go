package app

import (
	"context"
	_ "embed"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

//go:embed testdata/relation-tools-source.json
var relationToolsSource string

func relationToolDefinition() *jsjson.Object {
	v, err := jsjson.Parse(relationToolsSource)
	if err != nil {
		panic(err)
	}
	definition, _ := v.(*jsjson.Object).Get("definition")
	return definition.(*jsjson.Object)
}
func (a *Application) composeRelationExposure(o Owners) error {
	factory := a.options.relationExposurePort
	if factory == nil {
		factory = func(a *Application, o Owners) (*memoryservice.ExposurePort, error) {
			return memoryservice.NewExposurePort(a.Semantic.Memory, o.Sources, o.Main, o.Authority)
		}
	}
	port, err := factory(a, o)
	if err != nil {
		return err
	}
	a.RelationExposure, err = memoryservice.NewRelationExposure(port, a.MemoryExposure, nil)
	if err != nil {
		return err
	}
	// Exact source capture slot: ordinary memory, relation, commitments, identity.
	a.Engine.Runtime.AddExtension(&botruntime.Extension{CaptureToolResultGuards: func(c *botruntime.TurnContext) []*botruntime.ToolResultGuard {
		if guard := a.RelationExposure.CaptureLearningGuard(c); guard != nil {
			return []*botruntime.ToolResultGuard{guard}
		}
		return nil
	}})
	return nil
}
func (a *Application) registerRelationTool(memory *memoryOwners) {
	a.Engine.Runtime.AddExtension(&botruntime.Extension{
		ToolsForTurn: func(*botruntime.TurnContext) []*jsjson.Object {
			if memory.snapshot.enabled("memoryGraphEnabled") && memory.snapshot.enabled("memoryGraphAnswerEnabled") {
				return []*jsjson.Object{relationToolDefinition()}
			}
			return nil
		},
		Execute: func(ctx context.Context, name string, input any, c *botruntime.TurnContext, done func(any, error)) {
			if name != "memory.relations" {
				done(nil, errors.New("runtime_unknown_tool"))
				return
			}
			if err := a.Engine.TurnRead.GateTool(ctx, c); err != nil {
				done(nil, err)
				return
			}
			args, ok := input.(*jsjson.Object)
			if !ok {
				done(nil, errors.New("relation_input_required"))
				return
			}
			person := ""
			if raw, has := args.Get("personId"); has {
				var ok bool
				person, ok = raw.(string)
				if !ok {
					done(nil, errors.New("relation_person_id_invalid"))
					return
				}
			}
			records, err := a.RelationExposure.Query(ctx, c, person)
			if err == nil && !c.MemoryExposureDeferred() {
				records, err = a.RelationExposure.Deliver(ctx, c, memoryToolArray(records))
			}
			done(memoryToolArray(records), err)
		},
		Delivered: func(name string, value any, c *botruntime.TurnContext, input any) (any, error) {
			if name != "memory.relations" {
				return value, nil
			}
			if err := a.Engine.TurnRead.GateTool(context.Background(), c); err != nil {
				return nil, err
			}
			values, ok := value.([]any)
			if !ok {
				return []any{}, nil
			}
			records, err := a.RelationExposure.Deliver(context.Background(), c, values)
			if err != nil {
				return nil, err
			}
			result := memoryToolArray(records)
			if err = a.ExperienceHints.DeliveredTool(c, name, input, result); err != nil {
				return nil, err
			}
			return result, nil
		},
	})
}
