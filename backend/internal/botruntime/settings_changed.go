package botruntime

// ClearDeferredTopics implements runtime.ts clearDeferredTopics. It deliberately
// preserves overflow counters, routing metadata, acknowledged/protected messages,
// existing admissions and queued turns. Changing the setting does not replay or
// cancel previously accepted ingress.
func (r *Runtime) ClearDeferredTopics() error {
	done, ok := r.beginOperation()
	if !ok {
		return code("runtime_closed")
	}
	defer done()
	r.scenes.Range(func(_, value any) bool {
		s := value.(*sceneState)
		s.mu.Lock()
		s.routing.Deferred.Clear()
		r.publishLocked(s)
		s.mu.Unlock()
		return true
	})
	return nil
}

// ConfigureAdmission publishes the committed native settings and recomputes
// source admission deadlines. It retains ownership through trim and scheduling.
func (r *Runtime) ConfigureAdmission(settings Settings) error {
	done, ok := r.beginOperation()
	if !ok {
		return code("runtime_closed")
	}
	defer done()
	return r.Configure(settings)
}
