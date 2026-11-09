package app

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
)

func (a *Application) composeIdentityMemory(o Owners) error {
	port, err := memoryservice.NewExposurePort(a.Semantic.Memory, o.Sources, o.Main, o.Authority)
	if err != nil {
		return err
	}
	a.IdentityExposure, err = memoryservice.NewIdentityExposure(port, a.Engine.TurnRead)
	if err != nil {
		return err
	}
	base := botruntime.Extension{
		Tools: []*jsjson.Object{memoryToolObject("name", "memory.identities", "description", "Read administrator-verified identity links and scoped sourced aliases for people already authorized in this scene. Links are not personal facts or permission to read another scene. Original person/source identities remain unchanged. Keep qualification and validity; use the whole state receipt. Identity-state interpretation cannot be saved through contextual learning.", "inputSchema", memoryToolObject("type", "object", "properties", memoryToolObject("personId", memoryToolObject("type", "string", "minLength", float64(1), "maxLength", float64(160))), "required", []any{"personId"}, "additionalProperties", false))},
		Execute: func(ctx context.Context, name string, input any, c *botruntime.TurnContext, done func(any, error)) {
			value, err := a.executeIdentityMemory(ctx, name, input, c)
			done(value, err)
		},
	}
	a.Engine.Runtime.AddExtension(a.IdentityExposure.ComposeExtension(base))
	return nil
}
func (a *Application) executeIdentityMemory(ctx context.Context, name string, input any, c *botruntime.TurnContext) (any, error) {
	if name != "memory.identities" {
		return nil, errors.New("runtime_unknown_tool")
	}
	if err := a.Engine.TurnRead.GateTool(ctx, c); err != nil {
		return nil, err
	}
	args, ok := input.(*jsjson.Object)
	if !ok {
		return nil, errors.New("identity_tool_arguments_invalid")
	}
	raw, _ := args.Get("personId")
	person, ok := raw.(string)
	if !ok {
		return nil, errors.New("identity_tool_arguments_invalid")
	}
	length, err := jsjson.UTF16Len(person)
	if err != nil || length < 1 || length > 160 {
		return nil, errors.New("identity_tool_arguments_invalid")
	}
	values, err := a.IdentityExposure.Query(ctx, c, person)
	if err != nil {
		return nil, err
	}
	if c.MemoryExposureDeferred() {
		return memoryToolArray(values), nil
	}
	selected := make([]any, len(values))
	for i, v := range values {
		selected[i] = v
	}
	delivered, err := a.IdentityExposure.Deliver(ctx, c, selected)
	return memoryToolArray(delivered), err
}
