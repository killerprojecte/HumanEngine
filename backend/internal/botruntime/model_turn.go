package botruntime

import "context"

// TurnModelOwner captures the source Router before eligibility/context work.
// Runtime's existing turn-owned state releases it on every terminal path.
// Implementations must not hold scene or turn mutexes while reading config.
type TurnModelOwner interface {
	BeginTurn(context.Context, *TurnContext) error
}
