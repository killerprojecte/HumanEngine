package botruntime

// Observe adds the application logging sink without replacing the domain's Trace
// hook. The observer must return promptly and must not retain mutable event data.
// Passing nil detaches it. Atomic replacement permits application teardown.
func (r *Runtime) Observe(observer func(Event)) {
	if observer == nil {
		r.observer.Store(nil)
	} else {
		r.observer.Store(&observer)
	}
}
