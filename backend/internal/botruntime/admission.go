package botruntime

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/topicroute"
	"github.com/killerprojecte/HumanEngine/backend/internal/turnwait"
	"math"
	"strings"
)

func (r *Runtime) protectedLocked(s *sceneState, id string) bool {
	if s.tasks[id] != nil || s.pending[id] || s.timers[id] != nil {
		return true
	}
	if _, ok := s.queue.Get(id); ok {
		return true
	}
	if _, ok := s.routing.ProtectedSends.Get(id); ok {
		return true
	}
	if a := s.admissions[id]; a != nil {
		x := a.Retention()
		return x.Active || x.Pending || x.Waiting
	}
	return false
}
func (r *Runtime) attention(t *topic) float64 {
	return t.attention * math.Exp(-(r.services.Clock.Now()-t.lastAt)/300000)
}
func (r *Runtime) evictLocked(s *sceneState, id string) {
	s.topics.Delete(id)
	if a := s.admissions[id]; a != nil {
		a.Cancel()
		delete(s.admissions, id)
	}
	s.routing.Evict(id)
}
func (r *Runtime) topicLocked(s *sceneState, m *Object) (*topic, error) {
	cfg := r.config()
	now := r.services.Clock.Now()
	var chosen *topic
	if !cfg.TopicAdmissionEnabled {
		reply := str(get(m, "replyToId"))
		for _, e := range s.topics.Entries() {
			if reply != "" && contains(e.Value.messageIDs, reply) {
				chosen = e.Value
				break
			}
		}
		if chosen == nil {
			for _, e := range s.topics.Entries() {
				t := e.Value
				if now-t.lastAt < 120000 && (chosen == nil || r.attention(t) > r.attention(chosen)) {
					chosen = t
				}
			}
		}
		if chosen != nil {
			return chosen, nil
		}
	} else {
		for s.topics.Len() > 4 {
			var idle *topic
			for _, e := range s.topics.Entries() {
				t := e.Value
				if !r.protectedLocked(s, t.id) && (idle == nil || t.lastAt < idle.lastAt) {
					idle = t
				}
			}
			if idle == nil {
				return nil, nil
			}
			r.evictLocked(s, idle.id)
		}
		snapshots := []topicroute.TopicSnapshot{}
		for _, e := range s.topics.Entries() {
			t := e.Value
			participants := []string{}
			for _, row := range s.messages {
				if contains(t.messageIDs, idOf(row)) && get(row, "status") == "available" && get(row, "senderId") != "bot" {
					participants = append(participants, str(get(row, "senderId")))
				}
			}
			meta, _ := s.routing.Metadata.Get(t.id)
			snapshot := topicroute.TopicSnapshot{ID: t.id, SceneID: s.scene.ID, TouchedAt: t.lastAt, Participants: unique(participants), Active: s.tasks[t.id] != nil, Pending: r.protectedLocked(s, t.id), SendState: "none"}
			if _, ok := s.routing.ProtectedSends.Get(t.id); ok {
				snapshot.SendState = "unknown"
			}
			if meta != nil {
				snapshot.Provisional = meta.Provisional
				snapshot.Cue = meta.Cue
			}
			snapshots = append(snapshots, snapshot)
		}
		anchors := []topicroute.TopicAnchor{}
		reply := str(get(m, "replyToId"))
		for _, row := range s.messages {
			if idOf(row) != reply || get(row, "status") != "available" || s.recalled[reply] {
				continue
			}
			if get(row, "senderId") == "bot" {
				if id, ok := s.routing.Acknowledged.Get(reply); ok {
					anchors = append(anchors, topicroute.TopicAnchor{Kind: "reply", TopicID: id})
				}
			} else {
				for _, e := range s.topics.Entries() {
					if contains(e.Value.messageIDs, reply) {
						anchors = append(anchors, topicroute.TopicAnchor{Kind: "reply", TopicID: e.Key})
					}
				}
			}
		}
		var cue *string
		parts := array(get(m, "parts"))
		if len(parts) > 0 && get(parts[0], "type") == "text" {
			var err error
			cue, err = topicroute.ParseTopicCue(str(get(parts[0], "text")))
			if err != nil {
				return nil, err
			}
		}
		assignment, err := topicroute.AssignTopic(topicroute.TopicInput{SceneID: s.scene.ID, SenderID: str(get(m, "senderId")), Now: now, Anchors: anchors, UnavailableReply: reply != "" && len(anchors) == 0, Cue: cue}, snapshots, topicroute.AdmissionConfig{LocaleCompare: r.services.Compare})
		if err != nil {
			return nil, err
		}
		if assignment.Kind == "deferred" {
			return nil, nil
		}
		if assignment.TopicID != nil {
			t, _ := s.topics.Get(*assignment.TopicID)
			return t, nil
		}
		if assignment.EvictTopicID != nil {
			if r.protectedLocked(s, *assignment.EvictTopicID) {
				return nil, nil
			}
			r.evictLocked(s, *assignment.EvictTopicID)
		}
		chosen = &topic{id: newID(), lastAt: now}
		s.topics.Set(chosen.id, chosen)
		meta := &topicroute.Metadata{Provisional: assignment.Kind == "provisional", Cue: assignment.Cue}
		if assignment.Cue != nil {
			id := idOf(m)
			meta.CueSourceID = &id
		}
		s.routing.Metadata.Set(chosen.id, meta)
		return chosen, nil
	}
	chosen = &topic{id: newID(), lastAt: now}
	s.topics.Set(chosen.id, chosen)
	if s.topics.Len() > 4 {
		var oldest *topic
		for _, e := range s.topics.Entries() {
			if oldest == nil || e.Value.lastAt < oldest.lastAt {
				oldest = e.Value
			}
		}
		if oldest != nil && s.tasks[oldest.id] == nil {
			s.topics.Delete(oldest.id)
		}
	}
	return chosen, nil
}
func (r *Runtime) routeLocked(s *sceneState, m *Object, schedule bool) (string, error) {
	t, err := r.topicLocked(s, m)
	if err != nil {
		return "", err
	}
	can := schedule && s.scene.ParticipationEnabled
	cfg := r.config()
	if t == nil {
		if can {
			s.routing.Defer(idOf(m), schedule)
		}
		return "", nil
	}
	s.routing.Deferred.Delete(idOf(m))
	addressed := false
	for _, p := range array(get(m, "parts")) {
		if get(p, "type") == "mention" && get(p, "userId") == "bot" {
			addressed = true
		}
	}
	for _, row := range s.messages {
		if idOf(row) == str(get(m, "replyToId")) && get(row, "senderId") == "bot" && (!cfg.TopicAdmissionEnabled || get(row, "status") == "available") {
			addressed = true
		}
	}
	if !contains(t.messageIDs, idOf(m)) {
		t.attention = math.Min(1, r.attention(t)+map[bool]float64{true: .35, false: .15}[addressed])
		t.version.Add(1)
		t.lastAt = r.services.Clock.Now()
		t.messageIDs = append(t.messageIDs, idOf(m))
	}
	if !can {
		if s.scene.Kind != "simulation" && !cfg.CacheEnabled {
			r.clearBodiesLocked(s)
		}
		return t.id, nil
	}
	a := s.admissions[t.id]
	if a == nil {
		a, err = turnwait.New(r.waitConfig(), r.services.Clock.Now)
		if err != nil {
			return "", err
		}
		s.admissions[t.id] = a
	}
	outcome, err := a.Observe(turnwait.Event{ID: idOf(m), Meaningful: true, Addressed: addressed})
	if err != nil {
		return "", err
	}
	if cfg.TopicAdmissionEnabled && outcome != "accepted" {
		if outcome == "full" {
			s.routing.Defer(idOf(m), true)
		}
		return t.id, nil
	}
	s.queue.Set(t.id, struct{}{})
	if s.tasks[t.id] != nil {
		s.pending[t.id] = true
	}
	r.scheduleLocked(s, t.id)
	return t.id, nil
}
func (r *Runtime) waitConfig() turnwait.Config {
	c := r.config()
	return turnwait.Config{QuietMS: math.Min(c.DebounceMS, c.MaxWaitMS), MaxWaitMS: c.MaxWaitMS, AddressedQuietMS: math.Min(c.DebounceMS, 500), MaxModelWaitMS: 60000, MaxPending: 60}
}
func (r *Runtime) drainDeferred(s *sceneState) (err error) {
	defer func() {
		if err != nil && strings.HasPrefix(err.Error(), "recovery_") {
			err = nil
		}
	}()
	if !r.config().TopicAdmissionEnabled || s.closed.Load() {
		return nil
	}
	if err := r.services.Authority.Ready(); err != nil {
		return err
	}
	scene := s.view.Load().scene
	enabled, err := r.services.Authority.SceneParticipationEnabled(scene)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if scene.Kind != "simulation" {
		if _, ok := r.adapters.Load(scene.AdapterID); !ok {
			return nil
		}
	}
	s.mu.Lock()
	pending := s.routing.Deferred.Entries()
	s.mu.Unlock()
	for _, p := range pending {
		s.mu.Lock()
		var message *Object
		for _, m := range s.messages {
			if idOf(m) == p.Key && get(m, "status") == "available" {
				message = m
				break
			}
		}
		s.mu.Unlock()
		valid := message != nil
		var err error
		if valid {
			var recalled bool
			recalled, err = r.recalled(s, p.Key)
			valid = !recalled
		}
		if err == nil && valid && scene.Kind != "simulation" {
			valid, err = r.services.Authority.ValidateSources(scene, []*Object{message}, nil)
		}
		if err != nil {
			return err
		}
		s.mu.Lock()
		if !valid {
			s.routing.ForgetMessage(p.Key)
		} else {
			_, _ = r.routeLocked(s, message, p.Value.Schedule)
		}
		r.publishLocked(s)
		s.mu.Unlock()
	}
	return nil
}
