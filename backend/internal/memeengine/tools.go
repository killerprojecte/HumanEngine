package memeengine

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
)

func MemeTools() []*Object {
	stringSchema := func() *Object { return fields("type", "string") }
	return []*Object{
		fields("name", "memes.search", "description", "Search analyzed available memes by intent without re-running vision.", "inputSchema", fields("type", "object", "properties", fields("query", fields("type", "string", "maxLength", float64(500)), "tags", fields("type", "array", "items", stringSchema(), "maxItems", float64(10))), "required", []any{"query"}, "additionalProperties", false)),
		fields("name", "memes.feedback", "description", "Record explicit nonbot reaction to a meme; never infer approval from own usage or silence.", "inputSchema", fields("type", "object", "properties", fields("assetId", stringSchema(), "positive", fields("type", "boolean"), "sourceMessageId", stringSchema()), "required", []any{"assetId", "positive", "sourceMessageId"}, "additionalProperties", false)),
	}
}
func (s *Service) executeMeme(ctx context.Context, name string, input any, turn *botruntime.TurnContext) (any, error) {
	if e := s.TurnRead.GateTool(ctx, turn); e != nil {
		return nil, e
	}
	o, ok := input.(*Object)
	if !ok {
		return nil, errors.New("meme_tool_arguments_invalid")
	}
	c := memeservice.Context{SceneID: turn.Scene.ID, Simulation: turn.Mode == "simulation"}
	if name == "memes.search" {
		query, ok := get(o, "query").(string)
		if !ok {
			return nil, errors.New("meme_tool_arguments_invalid")
		}
		n, e := jsjson.CodePointCount(query)
		if e != nil || n > 500 {
			return nil, errors.New("meme_tool_arguments_invalid")
		}
		tags := []string{}
		for _, k := range o.Keys() {
			if k != "query" && k != "tags" {
				return nil, errors.New("meme_tool_arguments_invalid")
			}
		}
		if raw, has := o.Get("tags"); has {
			values, ok := raw.([]any)
			if !ok || len(values) > 10 {
				return nil, errors.New("meme_tool_arguments_invalid")
			}
			for _, v := range values {
				tag, ok := v.(string)
				if !ok {
					return nil, errors.New("meme_tool_arguments_invalid")
				}
				tags = append(tags, tag)
			}
		}
		assets, e := s.Memes.Search(ctx, c, query, tags)
		if e != nil {
			return nil, e
		}
		items := make([]any, len(assets))
		for i, asset := range assets {
			items[i] = asset
			id := str(get(asset, "id"))
			turn.AddScopedValidation(func(reads botruntime.ReadAuthority) (bool, error) {
				return s.Memes.VisibleInRuntimeScope(s.Sources, reads, c, id)
			})
		}
		return fields("items", items, "coverage", "partial", "scope", "query_candidates", "limit", float64(12)), nil
	}
	if name != "memes.feedback" || o.Len() != 3 {
		return nil, errors.New("meme_tool_arguments_invalid")
	}
	id, ok := get(o, "assetId").(string)
	if !ok {
		return nil, errors.New("meme_tool_arguments_invalid")
	}
	message, ok := get(o, "sourceMessageId").(string)
	if !ok {
		return nil, errors.New("meme_tool_arguments_invalid")
	}
	positive, ok := get(o, "positive").(bool)
	if !ok {
		return nil, errors.New("meme_tool_arguments_invalid")
	}
	has := turn.HasSourceSnapshots()
	messages := turn.Sources()
	if !has {
		snapshot, e := s.Runtime.Snapshot(turn.Scene.ID)
		if e != nil {
			return nil, e
		}
		messages = snapshot.Messages
	}
	return s.Memes.FeedbackFromSnapshots(ctx, c, id, message, positive, messages, has, s.Sources)
}

// ComposeExtension replaces only the Meme tools and preserves all other Engine
// hooks, including Experience/semantic/identity owners. Install in place of base.
func (s *Service) ComposeExtension(base botruntime.Extension) *botruntime.Extension {
	ext := base
	ext.Tools = []*Object{}
	for _, tool := range base.Tools {
		if get(tool, "name") != "memes.search" && get(tool, "name") != "memes.feedback" {
			ext.Tools = append(ext.Tools, tool)
		}
	}
	ext.Tools = append(ext.Tools, MemeTools()...)
	ext.Execute = func(ctx context.Context, name string, input any, turn *botruntime.TurnContext, settle func(any, error)) {
		if name != "memes.search" && name != "memes.feedback" {
			if base.Execute != nil {
				base.Execute(ctx, name, input, turn, settle)
			} else {
				settle(nil, errors.New("unknown_tool"))
			}
			return
		}
		root, release, e := s.RootContext(ctx)
		if e != nil {
			settle(nil, e)
			return
		}
		defer release()
		v, e := s.executeMeme(root, name, input, turn)
		settle(v, e)
	}
	s.composeOutgoing(&ext)
	s.composeToolResultGuards(&ext, base.CaptureToolResultGuards)
	return &ext
}
