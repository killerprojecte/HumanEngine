package botruntime

import (
	"math"
)

func (r *Runtime) clearBodiesLocked(s *sceneState) {
	s.messages = nil
	s.byID = map[string]*messageRecord{}
	s.byBody = map[*Object]*messageRecord{}
	s.available = map[string]bool{}
	s.earliest = math.Inf(1)
	s.history = nil
	for _, e := range s.routing.Metadata.Entries() {
		e.Value.Cue = nil
		e.Value.CueSourceID = nil
	}
	s.routing.Acknowledged.Clear()
	r.pruneHistoryLocked(s, math.Inf(1), 0)
}
func (r *Runtime) trimLocked(s *sceneState, pending bool) {
	c := r.config()
	if !c.CacheEnabled && !pending && len(s.tasks) == 0 && len(s.timers) == 0 {
		r.clearBodiesLocked(s)
		return
	}
	cutoff := r.services.Clock.Now() - r.lifetime(c)
	bodyDirty := len(s.messages) > 500 || !(s.earliest >= cutoff)
	if !bodyDirty && !s.historyDirty && s.historyEarliest >= cutoff && s.historyIDs.Len() <= c.TopicHistoryMaxMessages {
		return
	}
	if bodyDirty {
		s.trimVisits += uint64(len(s.messages))
		rows := suffix(filterMessages(s.messages, func(m *Object) bool { return s.byBody[m].receivedAt >= cutoff }), 500)
		s.messages = append([]*Object{}, rows...)
		r.rebuildMessagesLocked(s)
	}

	retained := map[string]bool{}
	for _, m := range s.messages {
		if get(m, "status") == "available" {
			retained[idOf(m)] = true
		}
	}
	for _, e := range s.routing.Metadata.Entries() {
		if e.Value.CueSourceID != nil && !retained[*e.Value.CueSourceID] {
			e.Value.Cue = nil
			e.Value.CueSourceID = nil
		}
	}
	for _, e := range s.routing.Acknowledged.Entries() {
		if !retained[e.Key] {
			s.routing.Acknowledged.Delete(e.Key)
		}
	}
	r.pruneHistoryLocked(s, cutoff, c.TopicHistoryMaxMessages)
}
func (r *Runtime) pruneHistoryLocked(s *sceneState, cutoff float64, max int) {
	protected, preferred := map[string]bool{}, map[string]bool{}
	for _, e := range s.routing.Deferred.Entries() {
		protected[e.Key] = true
	}
	for _, a := range s.admissions {
		for _, id := range a.RetainedEventIDs() {
			protected[id] = true
		}
	}
	for _, t := range s.tasks {
		for _, id := range t.context.dependencyIDs() {
			protected[id] = true
		}
		t.context.mu.Lock()
		for _, m := range t.context.sources {
			protected[idOf(m)] = true
		}
		t.context.mu.Unlock()
	}
	for _, m := range s.messages {
		preferred[idOf(m)] = true
		at := s.byBody[m].receivedAt
		if idOf(m) != "" && !math.IsNaN(at) {
			s.historyIDs.Set(idOf(m), at)
		}
	}
	unprotected := 0
	for _, e := range s.historyIDs.Entries() {
		if protected[e.Key] {
			continue
		}
		if e.Value < cutoff {
			s.historyIDs.Delete(e.Key)
		} else {
			unprotected++
		}
	}
	for _, e := range s.historyIDs.Entries() {
		if unprotected <= max {
			break
		}
		if !protected[e.Key] && !preferred[e.Key] {
			s.historyIDs.Delete(e.Key)
			unprotected--
		}
	}
	s.historyEarliest = math.Inf(1)
	for _, e := range s.historyIDs.Entries() {
		if !protected[e.Key] && e.Value < s.historyEarliest {
			s.historyEarliest = e.Value
		}
	}
	s.historyDirty = false
	for _, e := range s.topics.Entries() {
		keep := []string{}
		for _, id := range e.Value.messageIDs {
			_, found := s.historyIDs.Get(id)
			if found || protected[id] {
				keep = append(keep, id)
			}
		}
		e.Value.messageIDs = keep
	}
}
