package modelread

import "github.com/killerprojecte/HumanEngine/backend/internal/botruntime"

// CaptureScopedGuard snapshots the same immutable ticket as CaptureGuard, but
// checks through the receiver's already-held read authority. It never opens a
// second root publication while a tool-cache read holds the first one.
func (p *RuntimePolicy) CaptureScopedGuard(c *botruntime.TurnContext) (func(botruntime.ReadAuthority) (bool, error), bool) {
	state := p.turn(c)
	state.mu.Lock()
	ticket := state.ticket
	state.mu.Unlock()
	if ticket == nil {
		return nil, false
	}
	return func(reads botruntime.ReadAuthority) (bool, error) { return p.validScoped(reads, ticket) }, true
}
