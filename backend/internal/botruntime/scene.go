package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/turnwait"
	"math"
	"sort"
	"strings"
)

func (r *Runtime) state(id string) *sceneState {
	v, ok := r.scenes.Load(id)
	if !ok {
		return nil
	}
	return v.(*sceneState)
}
func (r *Runtime) sceneRegistration(scene Scene) *sceneRegistration {
	registration := &sceneRegistration{}
	registration.scene.Store(&scene)
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.closed.Load() {
		return nil
	}
	value, _ := r.transports.LoadOrStore(scene.ID, registration)
	return value.(*sceneRegistration)
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
	registration := r.sceneRegistration(scene)
	if registration == nil {
		return code("runtime_closed")
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
	var existing []*sceneState
	r.scenes.Range(func(_, v any) bool {
		s := v.(*sceneState)
		if s.view.Load().scene.Kind == "simulation" {
			existing = append(existing, s)
		}
		return true
	})
	if len(existing) >= 64 {
		sort.SliceStable(existing, func(i, j int) bool { return existing[i].sequence < existing[j].sequence })
		r.EndSimulation(existing[0].view.Load().scene.ID)
	}
	id := "sim:" + newID()
	return id, r.RegisterScene(Scene{SceneRef: dispatch.SceneRef{ID: id, Kind: "simulation", AdapterID: "simulation", AccountID: "simulation", PlatformSceneID: id}})
}
func (r *Runtime) Simulate(ctx context.Context, id, text string) (Snapshot, error) {
	s := r.state(id)
	if s == nil || s.view.Load().scene.Kind != "simulation" {
		return Snapshot{}, code("simulation_missing")
	}
	if !s.simulationAdmission.TryLock() {
		return Snapshot{}, code("simulation_busy")
	}
	defer s.simulationAdmission.Unlock()
	if s.view.Load().active > 0 {
		return Snapshot{}, code("simulation_busy")
	}
	now := iso(r.services.Clock.Now())
	m := obj("id", newID(), "sceneId", id, "senderId", "simulation-user", "sentAt", now, "receivedAt", now, "origin", "simulation", "parts", []any{obj("type", "text", "text", text)}, "status", "available")
	topic, err := r.Ingest(ctx, id, m, false)
	if err != nil {
		return Snapshot{}, err
	}
	h, err := r.Run(id, topic, false)
	if err != nil {
		return Snapshot{}, err
	}
	h.Wait()
	return r.Snapshot(id)
}
