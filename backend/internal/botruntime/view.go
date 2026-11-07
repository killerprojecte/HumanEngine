package botruntime

import (
	"math"
)

type TopicView struct {
	ID                string
	Version           uint64
	MessageCount      int
	Protected         bool
	Attention, LastAt float64
}
type sceneView struct {
	scene                              Scene
	history                            *messageBlock
	topics                             []TopicView
	active, deferred, pending, waiting int
	protected                          []string
	recalled                           map[string]bool
	closed                             bool
}
type Snapshot struct {
	Scene                              Scene
	Messages                           []*Object
	Topics                             []TopicView
	Active, Deferred, Pending, Waiting int
	Protected                          []string
	Closed                             bool
}

func (r *Runtime) publishLocked(s *sceneState) {
	v := &sceneView{scene: s.scene, history: s.history, active: len(s.tasks), deferred: s.routing.Deferred.Len(), pending: s.queue.Len(), recalled: s.recalled, closed: s.closed.Load()}
	available := s.available
	for _, e := range s.topics.Entries() {
		t := e.Value
		count := 0
		for _, id := range t.messageIDs {
			if available[id] {
				count++
			}
		}
		v.topics = append(v.topics, TopicView{t.id, t.version.Load(), count, r.protectedLocked(s, t.id), t.attention, t.lastAt})
	}
	for _, a := range s.admissions {
		if a.Retention().Waiting {
			v.waiting++
		}
	}
	for _, e := range s.routing.ProtectedSends.Entries() {
		v.protected = append(v.protected, e.Key)
	}
	s.view.Store(v)
}

// Snapshot never enters the scene owner or any model/tool/transport queue. Body
// cloning and fresh display authority projection happen in the reader's goroutine.
func (r *Runtime) Snapshot(id string) (Snapshot, error) {
	s := r.state(id)
	if s == nil {
		return Snapshot{}, code("scene_missing")
	}
	v := s.view.Load()
	var rows []*Object
	cfg := r.config()
	if v.scene.Kind != "simulation" {
		cutoff := r.services.Clock.Now() - r.lifetime(cfg)
		rows = []*Object{}
		for _, record := range bodyRecords(v.history) {
			if record.receivedAt >= cutoff {
				rows = append(rows, record.body)
			}
		}
	} else {
		rows = bodyRows(v.history)
	}
	copyRows := make([]*Object, len(rows))
	for i, m := range rows {
		copyRows[i] = cloneJSON(m).(*Object)
	}
	{
		recalled := []string{}
		for id := range v.recalled {
			recalled = append(recalled, id)
		}
		var err error
		copyRows, err = r.services.Authority.ProjectDisplay(v.scene, copyRows, recalled)
		if err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{v.scene, copyRows, append([]TopicView{}, v.topics...), v.active, v.deferred, v.pending, v.waiting, append([]string{}, v.protected...), v.closed}, nil
}
func (r *Runtime) date(s string) float64 {
	ms, ok, err := r.services.Dates.ParseMillis(s)
	if err != nil || !ok {
		return math.NaN()
	}
	return float64(ms)
}
func (r *Runtime) lifetime(c Settings) float64 {
	if c.CacheEnabled {
		return c.CacheHours * 3600000
	}
	return 60000
}
func (r *Runtime) recalled(s *sceneState, id string) (bool, error) {
	v := s.view.Load()
	if v.recalled[id] {
		return true, nil
	}
	return r.services.Authority.IsRecalled(v.scene, id)
}

func (r *Runtime) unrecalled(s *sceneState, messages []*Object) ([]*Object, error) {
	out := make([]*Object, 0, len(messages))
	for _, m := range messages {
		recalled, err := r.recalled(s, idOf(m))
		if err != nil {
			return nil, err
		}
		if !recalled {
			out = append(out, m)
		}
	}
	return out, nil
}
