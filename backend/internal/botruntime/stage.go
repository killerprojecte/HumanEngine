package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
	"sync"
)

type stage struct {
	context.Context
	signal *dispatch.Signal
	cancel context.CancelCauseFunc
	mu     sync.Mutex
	timer  modelservice.Timer
	stop   func() bool
	closed bool
}

func (r *Runtime) newStage(t *turn, role string) *stage {
	ctx, cancel := context.WithCancelCause(context.Background())
	if t.proactive {
		ctxWithPriority, _ := scheduler.WithModelPriority(ctx, 2)
		ctx = ctxWithPriority
	}
	s := &stage{Context: ctx, cancel: cancel, signal: dispatch.NewSignal()}
	s.stop = context.AfterFunc(t.context.root, func() { s.abort(code("context_cancelled")) })
	ms := t.settings.PlannerTimeoutMS
	if role == "replyer" {
		ms = t.settings.ReplyerTimeoutMS
	}
	s.mu.Lock()
	s.timer = r.services.Clock.AfterFunc(ms, func() { s.abort(code("model_stage_timeout")) })
	s.mu.Unlock()
	if t.context.root.Aborted() {
		s.abort(code("context_cancelled"))
	}
	t.context.stage.Store(s)
	return s
}
func (s *stage) abort(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.cancel(err)
	s.mu.Unlock()
	s.signal.Abort(err)
}
func (s *stage) progress() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}
func (s *stage) close() {
	s.mu.Lock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()
	s.stop()
}
func (r *Runtime) eligible(c *TurnContext) bool {
	if c.root.Aborted() || c.Signal().Aborted() || r.closed.Load() {
		return false
	}
	s := r.state(c.Scene.ID)
	if s != c.owner.state || s.closed.Load() {
		return false
	}
	v := s.view.Load()
	if r.services.Authority.Ready() != nil || (c.Mode != "simulation" && !v.scene.ParticipationEnabled) {
		return false
	}
	c.mu.Lock()
	sources := append([]*Object{}, c.sources...)
	deps := []string{}
	for id := range c.dependencies {
		deps = append(deps, id)
	}
	checks := append([]func() bool{}, c.validations...)
	c.mu.Unlock()
	for _, check := range checks {
		if !check() {
			return false
		}
	}
	for _, id := range deps {
		if v.recalled[id] {
			return false
		}
	}
	for _, m := range sources {
		if v.recalled[idOf(m)] {
			return false
		}
	}
	return r.services.Authority.ValidateSources(v.scene, sources, deps)
}
func (r *Runtime) completion(t *turn, s *stage, role, stageName string, messages, tools []*Object) (Completion, error) {
	r.trace(Event{SceneID: t.context.Scene.ID, TurnID: t.context.TurnID, Stage: stageName, Status: "started"})
	return replyplan.WithinSignal(s, func(settle func(Completion, error)) {
		r.services.Models.Start(s, ModelRequest{role, stageName, messages, tools, t.context, s.progress}, settle)
	})
}
func (r *Runtime) executeTool(t *turn, ctx context.Context, name string, args any) (any, error) {
	if name == "messages.get" || name == "messages.around" || name == "messages.byUser" {
		return r.read(t, ctx, name, args)
	}
	for _, e := range r.exts() {
		for _, tool := range e.Tools {
			if get(tool, "name") == name {
				if e.Execute == nil {
					return nil, code("tool_unavailable")
				}
				return replyplan.WithinSignal(ctx, func(settle func(any, error)) { e.Execute(ctx, name, args, t.context, settle) })
			}
		}
	}
	return nil, code("tool_unavailable")
}
func (r *Runtime) promptAuthority(t *turn, tools []*Object) (any, error) {
	current := []*Object{}
	for _, e := range r.exts() {
		current = append(current, e.Tools...)
	}
	return r.services.Authority.PromptAuthority(t.context.Scene, current)
}
