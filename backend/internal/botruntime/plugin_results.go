package botruntime

// ToolResultGuard is a private native capability. Pointer identity allows a
// receiving turn to adopt each guard once without comparing Go functions. Its
// check must use the supplied fresh authority, not retain a transaction scope.
type ToolResultGuard struct {
	Check func(ReadAuthority) (bool, error)
}

// CaptureToolResultGuards invokes extensions without holding runtime registry
// locks, matching the source manager's immutable cache ownership capture.
func (r *Runtime) CaptureToolResultGuards(c *TurnContext) []*ToolResultGuard {
	result := []*ToolResultGuard{}
	for _, e := range r.exts() {
		if e.CaptureToolResultGuards != nil {
			result = append(result, e.CaptureToolResultGuards(c)...)
		}
	}
	return result
}

// Dependencies snapshots the ordered source dependency set. Mutation of the
// returned slice cannot change a live turn's authority decisions.
func (c *TurnContext) Dependencies() []string { return c.dependencyIDs() }

// SourceResultGuard captures source bodies and dependencies independently of
// stage cancellation: cached data may be read in a later turn of the same scope.
// The cache owner also captures extension guards and invalidates on plugin unload.
func (c *TurnContext) SourceResultGuard() (*ToolResultGuard, []string) {
	c.mu.Lock()
	sources := make([]*Object, len(c.sources))
	for i, m := range c.sources {
		sources[i] = cloneJSON(m).(*Object)
	}
	deps := append([]string{}, c.dependencyOrder...)
	c.mu.Unlock()
	seen := map[string]bool{}
	for _, id := range deps {
		seen[id] = true
	}
	for _, m := range sources {
		if id := idOf(m); !seen[id] {
			seen[id] = true
			deps = append(deps, id)
		}
	}
	scene := c.Scene
	retained := append([]string{}, deps...)
	return &ToolResultGuard{Check: func(reads ReadAuthority) (bool, error) { return reads.ValidateSources(scene, sources, retained) }}, deps
}

// CheckToolResultGuard uses current durable authority and never caches the
// authorization result. Calls may outlive a finished turn but not its runtime.
func (r *Runtime) CheckToolResultGuard(guard *ToolResultGuard) (bool, error) {
	if r.closed.Load() {
		return false, nil
	}
	return guard.Check(r.services.Authority)
}
