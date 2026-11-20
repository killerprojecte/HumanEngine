package botruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/turnwait"
	"math"
	"strings"
)

func (r *Runtime) state(id string) *sceneState {
	v, ok := r.scenes.Load(id)
	if !ok {
		return nil
	}
	return v.(*sceneState)
}

var errSimulationSceneCollision = errors.New("simulation_scene_collision")

func (r *Runtime) sceneRegistration(scene Scene) (*sceneRegistration, error) {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.closed.Load() {
		return nil, code("runtime_closed")
	}
	if value, loaded := r.transports.Load(scene.ID); loaded {
		binding := value.(*sceneRegistration)
		// An ID belongs to its registered runtime mode until that owner closes.
		if (binding.scene.Load().Kind == "simulation") != (scene.Kind == "simulation") {
			return nil, errSimulationSceneCollision
		}
		return binding, nil
	}
	binding := &sceneRegistration{}
	owned := scene
	binding.scene.Store(&owned)
	r.transports.Store(owned.ID, binding)
	return binding, nil
}

// RegisteredScene returns transport metadata without allocating message state.
func (r *Runtime) RegisteredScene(id string) (Scene, bool) {
	value, ok := r.transports.Load(id)
	if !ok {
		return Scene{}, false
	}
	return *value.(*sceneRegistration).scene.Load(), true
}

func (r *Runtime) newSceneState(scene Scene) *sceneState {
	s := &sceneState{byID: map[string]*messageRecord{}, byBody: map[*Object]*messageRecord{}, available: map[string]bool{}, earliest: math.Inf(1), historyEarliest: math.Inf(1), operationKeys: map[string]bool{}, sequence: r.sequence.Add(1), scene: scene, tasks: map[string]*turn{}, admissions: map[string]*turnwait.Coordinator{}, timers: map[string]*timerTicket{}, pending: map[string]bool{}, recalled: map[string]bool{}, lastAutomatic: map[string]float64{}, lastTouched: r.services.Clock.Now()}
	s.mu.before = func() { r.reconcilePublishedRetentionLocked(s) }
	s.mu.Lock()
	r.publishLocked(s)
	s.mu.Unlock()
	return s
}

func (r *Runtime) RegisterScene(scene Scene) error {
	done, accepted := r.beginOperation()
	if !accepted {
		return code("runtime_closed")
	}
	defer done()
	if r.closed.Load() {
		return code("runtime_closed")
	}
	if scene.Kind != "simulation" {
		if r.services.Authority == nil {
			return code("runtime_real_authority_required")
		}
		if err := r.services.Authority.Ready(); err != nil {
			return err
		}
	} else if !strings.HasPrefix(scene.ID, "sim:") {
		return code("simulation_scope_required")
	}
	registration, err := r.sceneRegistration(scene)
	if err != nil {
		// The source saves real configuration independently of an active
		// simulation, without replacing the in-process simulation.
		if err == errSimulationSceneCollision && scene.Kind != "simulation" {
			return nil
		}
		return err
	}
	registration.scene.Store(&scene)
	s := r.newSceneState(scene)
	r.lifecycleMu.Lock()
	if r.closed.Load() {
		r.lifecycleMu.Unlock()
		return code("runtime_closed")
	}
	old, loaded := r.scenes.LoadOrStore(scene.ID, s)
	r.lifecycleMu.Unlock()
	if loaded {
		p := old.(*sceneState)
		p.mu.Lock()
		p.scene = scene
		r.publishLocked(p)
		p.mu.Unlock()
		if !scene.ParticipationEnabled && scene.Kind != "simulation" {
			r.Cancel(scene.ID)
		}
	}
	for _, extension := range r.exts() {
		if extension.SceneChanged != nil {
			extension.SceneChanged(scene)
		}
	}
	return nil
}
func (r *Runtime) CreateSimulation() (string, error) {
	r.simulationCreationMu.Lock()
	defer r.simulationCreationMu.Unlock()
	var oldest *sceneState
	count := 0
	r.scenes.Range(func(_, v any) bool {
		s := v.(*sceneState)
		if s.view.Load().scene.Kind == "simulation" {
			count++
			if oldest == nil || s.sequence < oldest.sequence {
				oldest = s
			}
		}
		return true
	})
	if count >= 64 {
		r.endSimulation(oldest)
	}
	id := "sim:" + newID()
	return id, r.RegisterScene(Scene{SceneRef: dispatch.SceneRef{ID: id, Kind: "simulation", AdapterID: "simulation", AccountID: "simulation", PlatformSceneID: id}})
}

// SimulationMessages is a message-only projection of one simulation view.
func (r *Runtime) SimulationMessages(id string) ([]*Object, error) {
	s := r.state(id)
	if s == nil || s.closed.Load() {
		return nil, nil
	}
	view := s.view.Load()
	if view.scene.Kind != "simulation" {
		return nil, nil
	}
	return r.projectViewMessages(view)
}

func (r *Runtime) Simulate(ctx context.Context, id, text string) ([]*Object, error) {
	done, admitted := r.beginOperation()
	if !admitted {
		return nil, code("runtime_closed")
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := r.state(id)
	if s == nil {
		return nil, code("simulation_missing")
	}
	scene := s.view.Load().scene
	if scene.Kind != "simulation" {
		return nil, code("simulation_missing")
	}
	if !s.simulationAdmission.TryLock() {
		return nil, code("simulation_busy")
	}
	defer s.simulationAdmission.Unlock()
	if s.view.Load().active > 0 {
		return nil, code("simulation_busy")
	}
	now := iso(r.services.Clock.Now())
	m := obj("id", newID(), "sceneId", id, "senderId", "simulation-user", "sentAt", now, "receivedAt", now, "origin", "simulation", "parts", []any{obj("type", "text", "text", text)}, "status", "available")
	// This owned synthetic message stays bound to the admitted state, even
	// if DELETE or idle cleanup removes its registration during the request.
	if blocked, err := r.ingressBlocked(s, m); err != nil {
		return nil, err
	} else if blocked {
		return []*Object{}, nil
	}
	topic, err := r.accept(s, m, false, true)
	if err != nil {
		return nil, err
	}
	h, err := r.runScene(s, topic, false)
	if err != nil {
		return nil, err
	}
	h.Wait()
	view := s.view.Load()
	if s.closed.Load() || view.scene.Kind != "simulation" {
		return []*Object{}, nil
	}
	return r.projectViewMessages(view)
}
