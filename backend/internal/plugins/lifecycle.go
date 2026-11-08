package plugins

// RuntimeStop identifies the exact activation whose worker/process exited.
// Observers must only revoke their own registrations, never block on Host work.
type RuntimeStop struct {
	ID         string
	Generation uint64
}

func (h *V1Host) ObserveRuntimeStops(observer func(RuntimeStop)) func() {
	h.lifecycle.Lock()
	h.observerSequence++
	key := h.observerSequence
	if h.observers == nil {
		h.observers = map[uint64]func(RuntimeStop){}
	}
	h.observers[key] = observer
	h.lifecycle.Unlock()
	return func() { h.lifecycle.Lock(); delete(h.observers, key); h.lifecycle.Unlock() }
}
func (h *V1Host) notifyRuntimeStop(stop RuntimeStop) {
	if stop.ID == "" {
		return
	}
	h.lifecycle.Lock()
	observers := make([]func(RuntimeStop), 0, len(h.observers))
	for _, f := range h.observers {
		observers = append(observers, f)
	}
	h.lifecycle.Unlock()
	for _, f := range observers {
		f(stop)
	}
}
func (h *V1Host) RuntimeGeneration(id string) (uint64, bool) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	lease := h.enabled[id]
	if h.closed || lease == nil || lease.closing || !lease.ready || !protocolProcessLive(lease) {
		return 0, false
	}
	return lease.generation, true
}
func (h *V1Host) watchProcess(p *nodeProcess) {
	p.exitWatch.Do(func() {
		go func() {
			<-p.done
			h.lifecycle.Lock()
			keys := map[string]bool{}
			for _, lease := range h.enabled {
				if lease.process == p {
					keys[lease.key] = true
				}
			}
			for lease := range h.retired {
				if lease.process == p {
					keys[lease.key] = true
				}
			}
			h.lifecycle.Unlock()
			for key := range keys {
				h.workerExited(p, key)
			}
		}()
	})
}
