package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
)

func (r *Runtime) normalize(m *Object, scene Scene) *Object {
	out := cloneJSON(m).(*Object)
	if v, ok := r.adapters.Load(scene.AdapterID); ok {
		bot := v.(*Adapter).BotUserID
		if bot != "" {
			if get(out, "senderId") == bot {
				_ = out.Set("senderId", "bot")
			}
			for _, p := range array(get(out, "parts")) {
				if get(p, "type") == "mention" && get(p, "userId") == bot {
					_ = p.(*Object).Set("userId", "bot")
				}
			}
		}
	}
	return out
}
func (r *Runtime) Ingest(ctx context.Context, sceneID string, message *Object, schedule bool) (string, error) {
	s := r.state(sceneID)
	if s == nil {
		return "", code("scene_missing")
	}
	s.ingress.Lock()
	defer s.ingress.Unlock()
	scene := s.view.Load().scene
	m := r.normalize(message, scene)
	if get(m, "sceneId") != sceneID || get(m, "origin") == "history" {
		return "", nil
	}
	if s.closed.Load() {
		return "", nil
	}
	if recalled, err := r.recalled(s, idOf(m)); err != nil || recalled {
		return "", err
	}
	s.mu.Lock()
	duplicate := s.byID[idOf(m)] != nil
	s.mu.Unlock()
	if duplicate {
		return "", nil
	}
	if scene.Kind != "simulation" && get(m, "senderId") != "bot" {
		status, trusted, e := r.services.Authority.Observe(ctx, scene, m, nil)
		if e != nil {
			return "", e
		}
		if status != "new" && status != "capacity" {
			return "", nil
		}
		if trusted == nil {
			return "", code("source_authority_missing_snapshot")
		}
		m = cloneJSON(trusted).(*Object)
	}
	return r.accept(s, m, schedule, true)
}
func (r *Runtime) accept(s *sceneState, m *Object, schedule, received bool) (string, error) {
	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		return "", nil
	}
	s.lastTouched = r.services.Clock.Now()
	r.appendMessageLocked(s, m)
	r.trimLocked(s, true)
	r.publishLocked(s)
	scene := s.scene
	s.mu.Unlock()
	if scene.Kind != "simulation" {
		r.trace(Event{SceneID: scene.ID, Stage: "ingress", Status: "completed", Reason: "message_received"})
	}
	if get(m, "senderId") == "bot" {
		return "", nil
	}
	if received {
		for _, e := range r.exts() {
			if e.Received != nil {
				_ = e.Received(cloneJSON(m).(*Object), scene)
			}
		}
	}
	s.mu.Lock()
	id, err := r.routeLocked(s, m, schedule)
	r.publishLocked(s)
	s.mu.Unlock()
	if err == nil {
		err = r.drainDeferred(s)
	}
	return id, err
}
func (r *Runtime) Edit(ctx context.Context, sceneID string, message, edit *Object, schedule bool) (string, error) {
	s := r.state(sceneID)
	if s == nil {
		return "", code("scene_missing")
	}
	scene := s.view.Load().scene
	if scene.Kind == "simulation" {
		return "simulation", nil
	}
	if get(message, "origin") != "live" || get(message, "status") != "available" || get(message, "senderId") == "bot" {
		return "", code("source_event_edit_requires_live_user")
	}
	s.ingress.Lock()
	defer s.ingress.Unlock()
	status, current, err := r.services.Authority.Observe(ctx, scene, r.normalize(message, scene), edit)
	if err != nil || status != "edited" {
		return status, err
	}
	if current == nil {
		return status, code("source_authority_missing_snapshot")
	}
	id := idOf(current)
	var previous *Object
	var abort []*dispatch.Signal
	s.mu.Lock()
	if record := s.byID[id]; record != nil {
		previous = record.body
	}
	s.routing.ForgetMessage(id)
	for _, t := range s.tasks {
		if contains(t.context.dependencyIDs(), id) {
			abort = append(abort, t.context.root)
		}
	}
	for _, e := range s.topics.Entries() {
		if contains(e.Value.messageIDs, id) {
			e.Value.version.Add(1)
			e.Value.messageIDs = remove(e.Value.messageIDs, id)
			if a := s.admissions[e.Key]; a != nil {
				_ = a.Replace(id)
			}
		}
	}
	s.messages = filterMessages(s.messages, func(m *Object) bool { return idOf(m) != id })
	s.historyDirty = true
	r.rebuildMessagesLocked(s)
	r.publishLocked(s)
	s.mu.Unlock()
	for _, signal := range abort {
		signal.Abort(code("source_edited"))
	}
	for _, e := range r.exts() {
		if e.Edited != nil {
			if err := e.Edited(previous, current, scene); err != nil {
				return status, err
			}
		}
	}
	if err := r.cancelInvalidDerived("source_edited", r.services.Authority); err != nil {
		return status, err
	}
	_, err = r.accept(s, cloneJSON(current).(*Object), schedule, true)
	return status, err
}
func (r *Runtime) Recall(ctx context.Context, sceneID, id string) error {
	s := r.state(sceneID)
	if s == nil {
		return code("scene_missing")
	}
	s.ingress.Lock()
	defer s.ingress.Unlock()
	scene := s.view.Load().scene
	prepare := func(authority ReadAuthority) (func() error, error) {
		callbacks := []func() error{}
		for _, e := range r.exts() {
			if e.PrepareRecallScoped != nil || e.PrepareRecall != nil {
				var f func() error
				var err error
				if e.PrepareRecallScoped != nil {
					f, err = e.PrepareRecallScoped(authority, sceneID, id)
				} else {
					f, err = e.PrepareRecall(sceneID, id)
				}
				if err != nil {
					return nil, err
				}
				if f != nil {
					callbacks = append(callbacks, f)
					continue
				}
			}
			if e.RecalledScoped != nil || e.Recalled != nil {
				extension := e
				callbacks = append(callbacks, func() error {
					if extension.RecalledScoped != nil {
						return extension.RecalledScoped(authority, sceneID, id)
					}
					return extension.Recalled(sceneID, id)
				})
			}
		}
		return func() error {
			for _, f := range callbacks {
				if err := f(); err != nil {
					return err
				}
			}
			return r.cancelInvalidDerived("derived_source_recalled", authority)
		}, nil
	}
	apply := func() error {
		var abort []*dispatch.Signal
		s.mu.Lock()
		next := map[string]bool{}
		for k, v := range s.recalled {
			next[k] = v
		}
		next[id] = true
		s.recalled = next
		for _, a := range s.admissions {
			a.Recall(id)
		}
		s.routing.ForgetMessage(id)
		for i, m := range s.messages {
			if idOf(m) == id {
				n := cloneJSON(m).(*Object)
				_ = n.Set("parts", []any{})
				_ = n.Set("status", "recalled")
				s.messages[i] = n
				r.replaceRecordLocked(s, m, n)
			}
		}
		r.rebuildMessagesLocked(s)
		for _, e := range s.topics.Entries() {
			if contains(e.Value.messageIDs, id) {
				e.Value.version.Add(1)
			}
		}
		for _, t := range s.tasks {
			if contains(t.context.dependencyIDs(), id) {
				abort = append(abort, t.context.root)
			}
		}
		r.publishLocked(s)
		s.mu.Unlock()
		for _, signal := range abort {
			signal.Abort(code("source_recalled"))
		}
		return nil
	}
	var err error
	if scene.Kind == "simulation" {
		err = apply()
	} else {
		err = r.services.Authority.Recall(ctx, scene, id, prepare, apply)
	}
	if err == nil && scene.Kind == "simulation" {
		err = r.cancelInvalidDerived("derived_source_recalled", r.services.Authority)
	}
	return err
}
func (r *Runtime) cancelInvalidDerived(reason string, authority ReadAuthority) (err error) {
	r.scenes.Range(func(_, v any) bool {
		s := v.(*sceneState)
		s.mu.Lock()
		turns := []*turn{}
		for _, t := range s.tasks {
			turns = append(turns, t)
		}
		s.mu.Unlock()
		for _, t := range turns {
			t.context.mu.Lock()
			checks := append([]func(ReadAuthority) (bool, error){}, t.context.validations...)
			t.context.mu.Unlock()
			for _, f := range checks {
				valid, e := f(authority)
				if e != nil {
					err = e
					return false
				}
				if !valid {
					t.context.root.Abort(code(reason))
					break
				}
			}
		}
		return true
	})
	return err
}
