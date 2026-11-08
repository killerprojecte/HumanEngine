package memeengine

import (
	"context"
	"encoding/json"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
)

func managedPart(raw json.RawMessage) (string, error) {
	value, e := jsjson.ParseBytes(raw)
	if e != nil {
		return "", e
	}
	part := obj(value)
	if get(part, "type") != "media" {
		return "", nil
	}
	id := str(get(part, "assetId"))
	if len(id) != 64 {
		return "", nil
	}
	for _, v := range []byte(id) {
		if !(v >= 'a' && v <= 'f' || v >= '0' && v <= '9') {
			return "", nil
		}
	}
	return id, nil
}
func (s *Service) installOutgoing() {
	s.Runtime.AddHook(s.outgoingHook())
}
func (s *Service) outgoingHook() botruntime.Hook {
	return botruntime.Hook{ID: "media-scope", Priority: -100, Run: func(ctx context.Context, segments []dispatch.Segment, turn *botruntime.TurnContext, settle func(botruntime.HookResult, error)) {
		root, release, e := s.RootContext(ctx)
		if e != nil {
			settle(botruntime.HookResult{}, e)
			return
		}
		defer release()
		for _, segment := range segments {
			for _, part := range segment.Parts {
				id, e := managedPart(part)
				if e != nil {
					settle(botruntime.HookResult{}, e)
					return
				}
				if id != "" {
					if _, e = s.Memes.Original(root, memeservice.Context{SceneID: turn.Scene.ID, Simulation: turn.Mode == "simulation"}, id); e != nil {
						settle(botruntime.HookResult{}, e)
						return
					}
				}
			}
		}
		settle(botruntime.HookResult{Segments: segments}, nil)
	}}
}
func (s *Service) composeOutgoing(ext *botruntime.Extension) {
	ext.SegmentSentSequencers = append(ext.SegmentSentSequencers, botruntime.NewSegmentSentSequencer(s.recordOutgoing))
}
func (s *Service) recordOutgoing(scene botruntime.Scene, _ string, segment dispatch.Segment, _ dispatch.Receipt) {
	// This is a new runtime event root, not a resurrected canceled turn. Runtime
	// owns its physical callback through CloseAndWait, including all SQL work.
	root, release, e := s.RootContext(context.Background())
	if e != nil {
		return
	}
	defer release()
	for _, part := range segment.Parts {
		id, e := managedPart(part)
		if e != nil || id == "" {
			continue
		}
		_ = s.Memes.RecordUsage(root, memeservice.Context{SceneID: scene.ID}, id)
	}
}
