package botruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"sync"
)

type typingRegistration struct {
	wait dispatch.TypingDelay
	life context.Context
}

// InstallTypingDelay installs a generation's wait and lifetime. Removing it
// affects future sends; retiring the lifetime also cancels its prepared sends.
func (r *Runtime) InstallTypingDelay(life context.Context, wait dispatch.TypingDelay) (func(), error) {
	if life == nil || wait == nil {
		return nil, errors.New("typing_delay_required")
	}
	r.extensionMu.Lock()
	defer r.extensionMu.Unlock()
	if r.pluginTyping != nil || r.services.TypingDelay != nil {
		return nil, errors.New("typing_delay_already_registered")
	}
	entry := &typingRegistration{wait: wait, life: life}
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
func (r *Runtime) typingDelay() (dispatch.TypingDelay, context.Context) {
	r.extensionMu.RLock()
	defer r.extensionMu.RUnlock()
	if r.pluginTyping != nil {
		return r.pluginTyping.wait, r.pluginTyping.life
	}
	return r.services.TypingDelay, nil
}

// WaitTypingDelay is the trusted composition entry point for the currently
// installed policy. A caller retains the supplied signal for the complete wait.
func (r *Runtime) WaitTypingDelay(segment dispatch.Segment, signal *dispatch.Signal) *dispatch.Future[struct{}] {
	if wait, _ := r.typingDelay(); wait != nil {
		return wait(segment, signal)
	}
	return dispatch.Resolved(struct{}{})
}
