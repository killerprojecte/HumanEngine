package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"strings"
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
	scene, ok := r.RegisteredScene(sceneID)
	if !ok {
		return "", code("scene_missing")
	}
	return r.IngestScene(ctx, scene, message, schedule)
}

// IngestScene is the transport ingress entry point. Scene metadata is registered
// before routing persistence, without RegisterScene's lifecycle readiness/cancel.
func (r *Runtime) IngestScene(ctx context.Context, scene Scene, message *Object, schedule bool) (string, error) {
	done, admitted := r.beginOperation()
	if !admitted {
		return "", code("runtime_closed")
	}
	defer done()
	m := r.normalize(message, scene)
	if get(m, "sceneId") != scene.ID || get(m, "origin") == "history" {
		return "", nil
	}
	if r.closed.Load() {
		return "", code("runtime_closed")
	}
	if scene.Kind == "simulation" && !strings.HasPrefix(scene.ID, "sim:") {
		return "", code("simulation_scope_required")
	}
	registration := r.sceneRegistration(scene)
	if registration == nil {
		return "", code("runtime_closed")
	}
	registration.mu.Lock()
	defer registration.mu.Unlock()
	s := r.state(scene.ID)
	if s != nil {
		s.ingress.Lock()
		defer s.ingress.Unlock()
		s.mu.Lock()
		// Transport metadata does not replace persisted feature switches.
		scene.ParticipationEnabled = s.scene.ParticipationEnabled
		scene.LearningEnabled = s.scene.LearningEnabled
		scene.ProactiveEnabled = s.scene.ProactiveEnabled
		s.scene = scene
		r.publishLocked(s)
		s.mu.Unlock()
	}
	// Publish a distinct immutable value, never the local subsequently merged scene.
	registeredScene := scene
	registration.scene.Store(&registeredScene)
	if scene.Kind != "simulation" {
		if err := r.services.Authority.RouteIngress(ctx, scene, m); err != nil {
			return "", err
		}
	}
	if s == nil {
		created := r.newSceneState(scene)
		r.lifecycleMu.Lock()
		if r.closed.Load() {
			r.lifecycleMu.Unlock()
			return "", code("runtime_closed")
		}
		value, _ := r.scenes.LoadOrStore(scene.ID, created)
		r.lifecycleMu.Unlock()
		s = value.(*sceneState)
		s.ingress.Lock()
		defer s.ingress.Unlock()
	}
	if blocked, err := r.ingressBlocked(s, m); blocked || err != nil {
		return "", err
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
func (r *Runtime) ingressBlocked(s *sceneState, m *Object) (bool, error) {
	if s.closed.Load() {
		return true, nil
	}
	s.mu.Lock()
	duplicate := s.byID[idOf(m)] != nil
	s.mu.Unlock()
	// Match source short circuit and diagnostic reads: duplicates read recall
	// once, recalled messages twice, and closed scenes do not read it at all.
	recalled := false
	if !duplicate {
		var err error
		recalled, err = r.recalled(s, idOf(m))
		if err != nil {
			return true, err
		}
	}
	if duplicate || recalled {
		_, err := r.recalled(s, idOf(m))
		return true, err
	}
	return false, nil
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
	if !r.config().TopicAdmissionEnabled {
		s.mu.Lock()
		t, err := r.topicLocked(s, m)
		addressed := false
		if err == nil && t != nil {
			addressed = r.bookkeepTopicLocked(s, t, m)
		}
		r.publishLocked(s)
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
		if received {
			r.notifyReceived(m, scene)
		}
		can, err := r.canSchedule(s.view.Load().scene, schedule)
		s.mu.Lock()
		id := ""
		if err == nil && t != nil {
			id, err = r.queueRoutedTopicLocked(s, t, m, addressed, can)
		}
		r.publishLocked(s)
		s.mu.Unlock()
		return id, err
	}
	if received {
		r.notifyReceived(m, scene)
	}
	can, err := r.canSchedule(s.view.Load().scene, schedule)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	id, err := r.routeLocked(s, m, schedule, can)
	r.publishLocked(s)
	s.mu.Unlock()
	if err == nil {
		err = r.drainDeferred(s)
	}
	return id, err
}

func (r *Runtime) notifyReceived(m *Object, scene Scene) {
	for _, e := range r.exts() {
		if e.Received != nil {
			if err := e.Received(cloneJSON(m).(*Object), scene); err != nil {
				r.trace(Event{Level: "error", Module: "extension", Code: "receive_failed", SceneID: scene.ID})
			}
		}
	}
}

func (r *Runtime) Edit(ctx context.Context, sceneID string, message, edit *Object, schedule bool) (string, error) {
	done, admitted := r.beginOperation()
	if !admitted {
		return "", code("runtime_closed")
	}
	defer done()
	return r.editResult(ctx, sceneID, message, edit, schedule, nil, nil)
}

type DetailedSourceAuthority interface {
	ObserveDetailed(context.Context, Scene, *Object, *Object) (string, *Object, *Object, error)
}

func (r *Runtime) EditResult(ctx context.Context, sceneID string, message, edit *Object, schedule bool) (*Object, error) {
	done, admitted := r.beginOperation()
	if !admitted {
		return nil, code("runtime_closed")
	}
	defer done()
	var result *Object
	_, err := r.editResult(ctx, sceneID, message, edit, schedule, func(value *Object) { result = value }, nil)
	return result, err
}
func (r *Runtime) editResult(ctx context.Context, sceneID string, message, edit *Object, schedule bool, capture func(*Object), accepted *acceptedSourceEdit) (string, error) {
	s := r.state(sceneID)
	if s == nil {
		return "", code("scene_missing")
	}
	scene := s.view.Load().scene
	if scene.Kind == "simulation" {
		if capture != nil {
			capture(obj("status", "simulation", "bodyCoverage", "unavailable"))
		}
		return "simulation", nil
	}
	if get(message, "origin") != "live" || get(message, "status") != "available" || get(message, "senderId") == "bot" {
		return "", code("source_event_edit_requires_live_user")
	}
	s.ingress.Lock()
	defer s.ingress.Unlock()
	var status string
	var current *Object
	var err error
	if accepted != nil {
		status, current = accepted.status, accepted.current
		if capture != nil {
			capture(accepted.result)
		}
	} else if capture == nil {
		status, current, err = r.services.Authority.Observe(ctx, scene, cloneJSON(message).(*Object), edit)
	} else {
		detailed, ok := r.services.Authority.(DetailedSourceAuthority)
		if !ok {
			return "", code("runtime_detailed_source_authority_required")
		}
		var result *Object
		status, current, result, err = detailed.ObserveDetailed(ctx, scene, cloneJSON(message).(*Object), edit)
		if err == nil {
			capture(result)
		}
	}
	if err != nil || status != "edited" {
		return status, err
	}
	if current == nil {
		return status, code("source_authority_missing_snapshot")
	}
	acceptedRef := get(current, "sourceEventRef")
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
	// Accepted edits re-enter the same routing/admission boundary after edited
	// callbacks; do not Observe again and accidentally advance their revision.
	current = r.normalize(current, scene)
	if get(current, "sceneId") != scene.ID || get(current, "origin") == "history" {
		return status, nil
	}
	registration := r.sceneRegistration(scene)
	if registration == nil {
		return status, code("runtime_closed")
	}
	registeredScene := scene
	registration.scene.Store(&registeredScene)
	if err = r.services.Authority.RouteIngress(ctx, scene, current); err != nil {
		return status, err
	}
	if blocked, e := r.ingressBlocked(s, current); blocked || e != nil {
		return status, e
	}
	if get(current, "senderId") != "bot" {
		// The accepted revision object is restored after callbacks, as ingestObserved
		// restores acceptedEdit. Replacing the property does not replace that token.
		_ = current.Set("sourceEventRef", acceptedRef)
		valid, e := r.services.Authority.ValidateSources(scene, []*Object{current}, []string{idOf(current)})
		if e != nil {
			return status, e
		}
		if !valid {
			return status, code("source_event_changed")
		}
	}
	_, err = r.accept(s, cloneJSON(current).(*Object), schedule, true)
	return status, err
}
func (r *Runtime) Recall(ctx context.Context, sceneID, id string) error {
	done, admitted := r.beginOperation()
	if !admitted {
		return code("runtime_closed")
	}
	defer done()
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
