package botruntime

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"sync"
)

type typingRegistration struct{ wait dispatch.TypingDelay }

// InstallTypingDelay installs a real cancellation-aware delay owner. Removing
// the registration affects future sends; its owner must also cancel pending waits.
func (r *Runtime) InstallTypingDelay(wait dispatch.TypingDelay) (func(), error) {
	if wait == nil {
		return nil, errors.New("typing_delay_required")
	}
	r.extensionMu.Lock()
	defer r.extensionMu.Unlock()
	if r.pluginTyping != nil || r.services.TypingDelay != nil {
		return nil, errors.New("typing_delay_already_registered")
	}
	entry := &typingRegistration{wait: wait}
	r.pluginTyping = entry
	var once sync.Once
	return func() {
		once.Do(func() {
			r.extensionMu.Lock()
			if r.pluginTyping == entry {
				r.pluginTyping = nil
			}
			r.extensionMu.Unlock()
		})
	}, nil
}
func (r *Runtime) typingDelay() dispatch.TypingDelay {
	r.extensionMu.RLock()
	defer r.extensionMu.RUnlock()
	if r.pluginTyping != nil {
		return r.pluginTyping.wait
	}
	return r.services.TypingDelay
}

// WaitTypingDelay is the trusted composition entry point for the currently
// installed policy. A caller retains the supplied signal for the complete wait.
func (r *Runtime) WaitTypingDelay(segment dispatch.Segment, signal *dispatch.Signal) *dispatch.Future[struct{}] {
	if wait := r.typingDelay(); wait != nil {
		return wait(segment, signal)
	}
	return dispatch.Resolved(struct{}{})
}
