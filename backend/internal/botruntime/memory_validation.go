package botruntime

// ValidateMemoryContext follows the source memory-tool contract: check abort
// and registered validations, not additional participation/planning policies.
func (c *TurnContext) ValidateMemoryContext() (bool, error) {
	return c.ValidateMemoryContextWith(c.owner.runtime.services.Authority)
}

// ValidateMemoryContextWith is for trusted same-owner compositions already
// inside a read/write scope. Each validation receives exactly that authority.
func (c *TurnContext) ValidateMemoryContextWith(reads ReadAuthority) (bool, error) {
	if c.Signal().Aborted() {
		return false, nil
	}
	c.mu.Lock()
	checks := append([]func(ReadAuthority) (bool, error){}, c.validations...)
	c.mu.Unlock()
	for _, check := range checks {
		valid, e := check(reads)
		if e != nil || !valid {
			return valid, e
		}
	}
	return !c.Signal().Aborted(), nil
}
