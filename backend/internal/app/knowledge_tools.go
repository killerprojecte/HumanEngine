package app

import (
	"context"
	_ "embed"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"time"
)

//go:embed testdata/knowledge-tools-source.json
var knowledgeToolsSource string

func knowledgeToolDefinition() *jsjson.Object {
	v, err := jsjson.Parse(knowledgeToolsSource)
	if err != nil {
		panic(err)
	}
	d, _ := v.(*jsjson.Object).Get("definition")
	return d.(*jsjson.Object)
}
func (a *Application) composeKnowledgeMemory(o Owners) error {
	port, err := memoryservice.NewExposurePort(a.Semantic.Memory, o.Sources, o.Main, o.Authority)
	if err != nil {
		return err
	}
	a.KnowledgeExposure, err = memoryservice.NewKnowledgeExposure(port, a.Engine.TurnRead, func() int64 { return time.Now().UnixMilli() })
	if err != nil {
		return err
	}
	// Source advertisement is unconditional and precedes identity. Capture is
	// registered separately after identity, as in the engine's capture order.
	a.Engine.Runtime.AddExtension(&botruntime.Extension{Tools: []*jsjson.Object{knowledgeToolDefinition()}, Execute: func(ctx context.Context, name string, input any, c *botruntime.TurnContext, done func(any, error)) {
		if name != "memory.knowledge" {
			done(nil, errors.New("runtime_unknown_tool"))
			return
		}
		out, err := a.executeKnowledgeTool(ctx, c, input)
		done(out, err)
	}, Delivered: func(name string, v any, c *botruntime.TurnContext, input any) (any, error) {
		if name != "memory.knowledge" {
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
		out, err := a.KnowledgeExposure.Deliver(context.Background(), c, values)
		if err != nil {
			return nil, err
		}
		result := memoryToolArray(out)
		if err = a.ExperienceHints.DeliveredTool(c, name, input, result); err != nil {
			return nil, err
		}
		return result, nil
	}})
	return nil
}
func (a *Application) registerKnowledgeCapture() {
	a.Engine.Runtime.AddExtension(&botruntime.Extension{CaptureToolResultGuards: func(c *botruntime.TurnContext) []*botruntime.ToolResultGuard {
		if g := a.KnowledgeExposure.CaptureGuard(c); g != nil {
			return []*botruntime.ToolResultGuard{g}
		}
		return nil
	}})
}
func (a *Application) executeKnowledgeTool(ctx context.Context, c *botruntime.TurnContext, input any) (any, error) {
	if c.Mode == "real" {
		if _, err := a.Engine.TurnRead.Begin(ctx, c); err != nil {
			return nil, err
		}
	}
	query, err := knowledge.ParseQuery(input, true)
	if err != nil {
		return nil, err
	}
	anchor := contextualField(query, "anchor").(*jsjson.Object)
	kind := contextualField(anchor, "kind")
	if kind == "person" {
		if err = a.Engine.TurnRead.GateTool(ctx, c); err != nil {
			return nil, err
		}
	}
	var values []*jsjson.Object
	if kind == "memory" {
		id := contextualField(anchor, "memoryId").(string)
		var ref *jsjson.Object
		for _, r := range a.MemoryExposure.ReadExposed(c) {
			if r.ID == id {
				ref = memoryToolObject("id", r.ID, "version", r.Version, "deletionEpoch", r.DeletionEpoch)
				break
			}
		}
		exact := a.MemoryExposure.CaptureExactGuard(c, id)
		policy, ok := a.Engine.TurnRead.CaptureScopedGuard(c)
		if ref == nil || exact == nil || !ok {
			return []any{}, nil
		}
		guard := &botruntime.ToolResultGuard{Check: func(reads botruntime.ReadAuthority) (bool, error) {
			valid, err := exact.Check(reads)
			if err != nil || !valid {
				return false, err
			}
			return policy(reads)
		}}
		values, err = a.KnowledgeExposure.QueryFromBacking(ctx, c, ref, memoryToolObject("predicates", contextualField(query, "predicates"), "depth", contextualField(query, "depth")), guard)
	} else {
		values, err = a.KnowledgeExposure.Query(ctx, c, query)
	}
	if err == nil && !c.MemoryExposureDeferred() {
		values, err = a.KnowledgeExposure.Deliver(ctx, c, memoryToolArray(values))
	}
	return memoryToolArray(values), err
}
