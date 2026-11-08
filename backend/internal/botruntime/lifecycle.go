package botruntime

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/turnwait"
)

func (r *Runtime) Cancel(id string) {
	s := r.state(id)
	if s == nil {
		return
	}
	s.mu.Lock()
	signals := []*dispatch.Signal{}
	for _, ticket := range s.timers {
		ticket.timer.Stop()
	}
	s.timers = map[string]*timerTicket{}
	s.lastAutomatic = map[string]float64{}
	s.routing.Cancel()
	for _, a := range s.admissions {
		a.Cancel()
	}
	s.admissions = map[string]*turnwait.Coordinator{}
	s.historyDirty = true
	s.pending = map[string]bool{}
	s.queue.Clear()
	for _, t := range s.tasks {
		signals = append(signals, t.context.root)
	}
	r.publishLocked(s)
	s.mu.Unlock()
	for _, signal := range signals {
		signal.Abort(code("scene_cancelled"))
	}
}
func (r *Runtime) EndSimulation(id string) {
	s := r.state(id)
	if s == nil || s.view.Load().scene.Kind != "simulation" {
		return
	}
	r.Cancel(id)
	s.closed.Store(true)
	r.scenes.CompareAndDelete(id, s)
	r.transports.Delete(id)
	r.operationMu.Lock()
	for key := range s.operationKeys {
		r.deleteOperationLocked(key)
	}
	r.operationMu.Unlock()
}
func (r *Runtime) AddAdapter(id string, a *Adapter) {
	if old, loaded := r.adapters.Load(id); loaded {
		if r.config().TopicAdmissionEnabled {
			r.cancelAdapter(id)
		}
		if f := old.(*Adapter).Dispose; f != nil {
			f()
		}
	}
	r.adapters.Store(id, a)
}
func (r *Runtime) cancelAdapter(id string) {
	r.scenes.Range(func(k, v any) bool {
		if v.(*sceneState).view.Load().scene.AdapterID == id {
			r.Cancel(k.(string))
		}
		return true
	})
}
func (r *Runtime) RemoveAdapter(id string) {
	r.cancelAdapter(id)
	if old, ok := r.adapters.LoadAndDelete(id); ok && old.(*Adapter).Dispose != nil {
		old.(*Adapter).Dispose()
	}
}
func (r *Runtime) Close() {
	r.lifecycleMu.Lock()
	alreadyClosed := r.closed.Swap(true)
	var finishClose func()
	if !alreadyClosed {
		finishClose, _ = r.work.Start()
	}
	r.work.Seal()
	r.lifecycleMu.Unlock()
	if alreadyClosed {
		return
	}
	defer finishClose()
	for _, extension := range r.exts() {
		if extension.Closed != nil {
			extension.Closed()
		}
	}
	r.cleanupMu.Lock()
	if r.cleanup != nil {
		r.cleanup.Stop()
	}
	r.cleanupMu.Unlock()
	r.scenes.Range(func(k, v any) bool {
		s := v.(*sceneState)
		r.Cancel(k.(string))
		s.closed.Store(true)
		r.scenes.CompareAndDelete(k, s)
		return true
	})
	r.transports.Range(func(k, v any) bool { r.transports.Delete(k); return true })
	r.adapters.Range(func(k, v any) bool {
		r.adapters.Delete(k)
		if f := v.(*Adapter).Dispose; f != nil {
			f()
		}
		return true
	})
	r.cacheMu.Lock()
	r.windows.Clear()
	r.cacheMu.Unlock()
	r.boundaries.Clear()
	r.operationMu.Lock()
	for _, e := range r.operations.Entries() {
		r.deleteOperationLocked(e.Key)
	}
	r.operationMu.Unlock()
}
func (r *Runtime) armCleanup() {
	r.cleanupMu.Lock()
	defer r.cleanupMu.Unlock()
	if r.closed.Load() {
		return
	}
	r.cleanup = r.services.Clock.AfterFunc(60000, func() { r.CleanupIdle(); r.armCleanup() })
}
func (r *Runtime) CleanupIdle() {
	done, ok := r.work.Start()
	if !ok {
		return
	}
	defer done()
	now := r.services.Clock.Now()
	c := r.config()
	r.scenes.Range(func(k, v any) bool {
		s := v.(*sceneState)
		s.mu.Lock()
		if s.scene.Kind == "simulation" {
			expired := now-s.lastTouched > 3600000
			s.mu.Unlock()
			if expired {
				r.EndSimulation(k.(string))
			}
			return true
		}
		r.trimLocked(s, false)
		keep := now-s.lastTouched <= r.lifetime(c) || len(s.messages) > 0 || len(s.tasks) > 0 || len(s.timers) > 0 || len(s.pending) > 0 || s.queue.Len() > 0 || len(s.admissions) > 0 || s.routing.Deferred.Len() > 0 || s.routing.Turns.Len() > 0 || s.routing.ProtectedSends.Len() > 0 || len(s.recalled) > 0
		for kind, last := range s.lastAutomatic {
			delay := c.DirectCooldownMS
			if kind == "group" {
				delay = c.GroupCooldownMS
			}
			keep = keep || now < last+delay
		}
		if s.scene.ProactiveEnabled {
			for _, e := range s.topics.Entries() {
				keep = keep || now-e.Value.lastAt < 30*60000
			}
		}
		if !keep {
			s.closed.Store(true)
			r.scenes.CompareAndDelete(k, s)
		}
		r.publishLocked(s)
		s.mu.Unlock()
		return true
	})
}
func (r *Runtime) Configure(settings Settings) error {
	if settings.SceneConcurrency < 1 {
		return code("invalid_scene_concurrency")
	}
	if settings.Hour == nil {
		settings.Hour = r.config().Hour
	}
	settings.ParticipationWindows = append([]ParticipationWindow{}, settings.ParticipationWindows...)
	r.settings.Store(&settings)
	r.scenes.Range(func(_, v any) bool {
		s := v.(*sceneState)
		s.mu.Lock()
		if s.scene.Kind != "simulation" {
			r.trimLocked(s, false)
		}
		for _, e := range s.queue.Entries() {
			r.scheduleLocked(s, e.Key)
		}
		r.publishLocked(s)
		s.mu.Unlock()
		return true
	})
	return nil
}
