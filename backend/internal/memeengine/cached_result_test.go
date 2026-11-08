package memeengine

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"testing"
)

func TestToolResultCapturePreservesBaseHookWithoutPromotingOrdinaryValidation(t *testing.T) {
	s := &Service{}
	inherited := &botruntime.ToolResultGuard{Check: func(botruntime.ReadAuthority) (bool, error) { return true, nil }}
	calls := 0
	ext := s.ComposeExtension(botruntime.Extension{CaptureToolResultGuards: func(*botruntime.TurnContext) []*botruntime.ToolResultGuard {
		calls++
		return []*botruntime.ToolResultGuard{inherited}
	}})
	turn := &botruntime.TurnContext{}
	turn.AddValidation(func() bool { return false })
	guards := ext.CaptureToolResultGuards(turn)
	if calls != 1 || len(guards) != 1 || guards[0] != inherited {
		t.Fatal("base guard lost or duplicated", calls, guards)
	}
	guards[0] = nil
	if got := ext.CaptureToolResultGuards(turn); len(got) != 1 || got[0] != inherited {
		t.Fatal("capture slice leaked mutation")
	}
}
