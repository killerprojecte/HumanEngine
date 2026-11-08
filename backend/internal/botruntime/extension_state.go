package botruntime

import "errors"

// OwnedExtensionState ties private extension state to this turn's lifetime,
// rather than a process-wide map keyed by turn IDs. create must only allocate
// local state, never perform I/O or reenter the turn. key is an owner identity.
func (c *TurnContext) OwnedExtensionState(key any, create func() any) any {
	var value any
	var closeLate bool
	func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.extensionState == nil {
			c.extensionState = map[any]any{}
		}
		if existing, ok := c.extensionState[key]; ok {
			value = existing
			return
		}
		value = create()
		c.extensionState[key] = value
		closeLate = c.extensionStateClosed
	}()
	if closeLate {
		if owned, ok := value.(TurnStateReleaser); ok {
			_ = owned.ReleaseTurnState()
		}
	}
	return value
}

func (c *TurnContext) HasSourceSnapshots() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sources != nil
}

// TurnStateReleaser owns external continuation handles, not runtime locks.
type TurnStateReleaser interface{ ReleaseTurnState() error }

func (c *TurnContext) releaseExtensionState() error {
	c.mu.Lock()
	if c.extensionStateClosed {
		c.mu.Unlock()
		return nil
	}
	c.extensionStateClosed = true
	values := make([]any, 0, len(c.extensionState))
	for _, v := range c.extensionState {
		values = append(values, v)
	}
	c.mu.Unlock()
	var failures []error
	for _, value := range values {
		if owned, ok := value.(TurnStateReleaser); ok {
			failures = append(failures, owned.ReleaseTurnState())
		}
	}
	return errors.Join(failures...)
}
