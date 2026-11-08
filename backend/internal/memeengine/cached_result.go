package memeengine

import "github.com/killerprojecte/HumanEngine/backend/internal/botruntime"

// composeToolResultGuards preserves the Engine capture boundary: the model-read
// ticket is retained, but ordinary per-turn Meme visibility validations are not
// promoted into cached-result guards. See engine.ts captureToolResultGuards.
func (s *Service) composeToolResultGuards(ext *botruntime.Extension, inherited func(*botruntime.TurnContext) []*botruntime.ToolResultGuard) {
	ext.CaptureToolResultGuards = func(turn *botruntime.TurnContext) []*botruntime.ToolResultGuard {
		guards := []*botruntime.ToolResultGuard{}
		if s.TurnRead != nil {
			if check, ok := s.TurnRead.CaptureScopedGuard(turn); ok {
				guards = append(guards, &botruntime.ToolResultGuard{Check: check})
			}
		}
		if inherited != nil {
			guards = append(guards, inherited(turn)...)
		}
		return guards
	}
}
