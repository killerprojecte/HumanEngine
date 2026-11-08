package botruntime

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/turnwait"
	"math"
)

func (r *Runtime) automaticDueLocked(s *sceneState, a *turnwait.Coordinator) (float64, string) {
	addressed, present := a.PendingAddressed()
	kind := "direct"
	if s.scene.Kind == "group" && !addressed {
		kind = "group"
	}
	if s.scene.Kind == "simulation" || !present {
		return 0, kind
	}
	c := r.config()
	if len(s.tasks) >= c.SceneConcurrency {
		return math.Inf(1), kind
	}
	last, ok := s.lastAutomatic[kind]
	if !ok {
		return 0, kind
	}
	delay := c.DirectCooldownMS
	if kind == "group" {
		delay = c.GroupCooldownMS
	}
	return last + delay, kind
}
func (r *Runtime) scheduleLocked(s *sceneState, id string) {
	if old := s.timers[id]; old != nil {
		old.timer.Stop()
		delete(s.timers, id)
	}
	a := s.admissions[id]
	if a == nil || s.closed.Load() || s.tasks[id] != nil {
		return
	}
	_ = a.Configure(r.waitConfig())
	at, ok := a.NextWakeAt()
	if !ok {
		return
	}
	due, _ := r.automaticDueLocked(s, a)
	due = math.Max(at, due)
	if math.IsInf(due, 0) {
		return
	}
	ticket := &timerTicket{}
	s.timers[id] = ticket
	ticket.timer = r.services.Clock.AfterFunc(math.Max(0, due-r.services.Clock.Now()), func() { r.wake(s, id, ticket) })
}
func (r *Runtime) wake(s *sceneState, id string, ticket *timerTicket) {
	done, ok := r.work.Start()
	if !ok {
		return
	}
	defer done()
	if s.view.Load().scene.Kind != "simulation" && r.config().TopicAdmissionEnabled && r.services.Authority.Ready() != nil {
		s.mu.Lock()
		if s.timers[id] == ticket {
			delete(s.timers, id)
			r.publishLocked(s)
		}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	if s.timers[id] != ticket || s.closed.Load() {
		s.mu.Unlock()
		return
	}
	delete(s.timers, id)
	a := s.admissions[id]
	if a == nil {
		r.publishLocked(s)
		s.mu.Unlock()
		return
	}
	due, kind := r.automaticDueLocked(s, a)
	if due > r.services.Clock.Now() {
		r.scheduleLocked(s, id)
		r.publishLocked(s)
		s.mu.Unlock()
		return
	}
	wake := a.TakeReady()
	if wake == nil {
		r.scheduleLocked(s, id)
		r.publishLocked(s)
		s.mu.Unlock()
		return
	}
	if wake.Kind != "run" {
		r.scheduleLocked(s, id)
		r.publishLocked(s)
		s.mu.Unlock()
		r.trace(Event{SceneID: s.scene.ID, Stage: "turn_wait", Status: "skipped", Reason: "turn_wait_recheck"})
		if err := r.drainDeferred(s); err != nil {
			r.trace(Event{Level: "error", Module: "runtime", SceneID: s.scene.ID, Stage: "routing", Status: "failed", Reason: "source_read_failed"})
		}
		return
	}
	s.lastAutomatic[kind] = r.services.Clock.Now()
	t, err := r.startLocked(s, id, false, wake)
	r.publishLocked(s)
	s.mu.Unlock()
	if err == nil {
		go r.execute(t)
	}
}
func (r *Runtime) Run(sceneID, topicID string, proactive bool) (*TurnHandle, error) {
	s := r.state(sceneID)
	if s == nil {
		return nil, code("scene_missing")
	}
	s.mu.Lock()
	t, err := r.startLocked(s, topicID, proactive, nil)
	r.publishLocked(s)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	go r.execute(t)
	return &TurnHandle{t.done, t.result}, nil
}
func (r *Runtime) startLocked(s *sceneState, id string, proactive bool, wake *turnwait.Wake) (*turn, error) {
	if s.closed.Load() {
		return nil, code("scene_closed")
	}
	if s.tasks[id] != nil {
		return nil, code("topic_busy")
	}
	topic, ok := s.topics.Get(id)
	if !ok {
		return nil, code("topic_unavailable")
	}
	turnID := newID()
	c := &TurnContext{Scene: s.scene, Mode: "real", TurnID: turnID, root: dispatch.NewSignal(), dependencies: map[string]bool{}}
	if s.scene.Kind == "simulation" {
		c.Mode = "simulation"
	}
	records := map[*Object]*messageRecord{}
	t := &turn{records: records, runtime: r, state: s, topic: topic, context: c, settings: r.config(), wake: wake, proactive: proactive, version: topic.version.Load(), done: make(chan struct{}), result: &TurnResult{TurnID: turnID, Status: "completed", Reason: "processing_finished"}}
	c.owner = t
	r.lifecycleMu.Lock()
	if r.closed.Load() {
		r.lifecycleMu.Unlock()
		return nil, code("runtime_closed")
	}
	done, accepted := r.work.Start()
	r.lifecycleMu.Unlock()
	if !accepted {
		return nil, code("runtime_closed")
	}
	t.drainDone = done
	s.tasks[id] = t
	s.routing.Turns.Set(turnID, id)
	s.queue.Delete(id)
	return t, nil
}

// Proactive preserves manual group semantics; scheduled opportunities yield to
// already queued ingress and use the source's low-priority model context.
func (r *Runtime) Proactive(sceneID string, scheduled bool) (*TurnHandle, error) {
	s := r.state(sceneID)
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	if s.closed.Load() || s.scene.Kind != "group" || !s.scene.ParticipationEnabled || !s.scene.ProactiveEnabled {
		s.mu.Unlock()
		return nil, nil
	}
	cfg := r.config()
	if scheduled && (len(s.tasks) >= cfg.SceneConcurrency || s.queue.Len() > 0) {
		s.mu.Unlock()
		return nil, nil
	}
	if scheduled {
		if last, ok := s.lastAutomatic["group"]; ok && r.services.Clock.Now() < last+cfg.GroupCooldownMS {
			s.mu.Unlock()
			return nil, nil
		}
	}
	var selected *topic
	for _, e := range s.topics.Entries() {
		t := e.Value
		if r.services.Clock.Now()-t.lastAt < 30*60000 && (selected == nil || r.attention(t) > r.attention(selected)) {
			selected = t
		}
	}
	if selected == nil || s.tasks[selected.id] != nil {
		s.mu.Unlock()
		return nil, nil
	}
	if scheduled {
		s.lastAutomatic["group"] = r.services.Clock.Now()
	}
	t, err := r.startLocked(s, selected.id, true, nil)
	r.publishLocked(s)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	go r.execute(t)
	return &TurnHandle{t.done, t.result}, nil
}
