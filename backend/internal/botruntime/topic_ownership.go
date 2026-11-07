package botruntime

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/topicroute"
)

func (r *Runtime) admitted(t *turn, id string, original map[string]bool, owned bool) bool {
	s := t.state
	s.mu.Lock()
	defer s.mu.Unlock()
	_, deferred := s.routing.Deferred.Get(id)
	ack, _ := s.routing.Acknowledged.Get(id)
	return original[id] && !deferred && (!owned || contains(t.topic.messageIDs, id) || ack == t.topic.id)
}
func (r *Runtime) reassign(t *turn, decision *Object, recent []*Object, extensionDeps []string) ([]*Object, map[string]bool, error) {
	groups := array(get(decision, "topics"))
	if len(groups) == 0 {
		return recent, nil, nil
	}
	retained := []topicroute.Retained{}
	assignments := []topicroute.Group{}
	var selected *Object
	targets := stringsOf(get(decision, "targetMessageIds"))
	topicIDs := map[string]bool{}
	for _, v := range groups {
		g := v.(*Object)
		id := str(get(g, "topicId"))
		if topicIDs[id] {
			return nil, nil, code("planner_invalid_topic")
		}
		topicIDs[id] = true
		for _, m := range stringsOf(get(g, "messageIds")) {
			if !contains(ids(recent), m) {
				return nil, nil, code("planner_invalid_topic")
			}
		}
		assignments = append(assignments, topicroute.Group{TopicID: id, MessageIDs: stringsOf(get(g, "messageIds"))})
		if get(g, "topicId") == get(decision, "topicId") {
			selected = g
		}
	}
	if selected == nil {
		for _, v := range groups {
			g := v.(*Object)
			for _, target := range targets {
				if contains(stringsOf(get(g, "messageIds")), target) {
					selected = g
					break
				}
			}
			if selected != nil {
				break
			}
		}
	}
	s := t.state
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.topics.Entries() {
		retained = append(retained, topicroute.Retained{ID: e.Key, MessageIDs: e.Value.messageIDs, Protected: r.protectedLocked(s, e.Key)})
	}
	var selectedID *string
	if selected != nil {
		id := str(get(selected, "topicId"))
		selectedID = &id
	}
	if r.config().TopicAdmissionEnabled && !topicroute.ValidReassignment(topicroute.Reassignment{Groups: assignments, DeliveredIDs: ids(recent), CurrentTopicID: t.topic.id, SelectedTopicID: selectedID, Retained: retained}) {
		return nil, nil, code("planner_invalid_topic")
	}
	assigned := []string{}
	for _, g := range assignments {
		assigned = append(assigned, g.MessageIDs...)
	}
	for _, e := range s.topics.Entries() {
		remaining := []string{}
		for _, id := range e.Value.messageIDs {
			if !contains(assigned, id) {
				remaining = append(remaining, id)
			}
		}
		if len(remaining) != len(e.Value.messageIDs) {
			e.Value.version.Add(1)
			if e.Value == t.topic {
				t.version++
			}
		}
		e.Value.messageIDs = remaining
	}
	for i, g := range assignments {
		id := g.TopicID
		if groups[i] == selected {
			id = t.topic.id
		} else if id == t.topic.id {
			id = newID()
		}
		mapped, ok := s.topics.Get(id)
		if !ok {
			mapped = &topic{id: id, lastAt: r.services.Clock.Now(), attention: .4}
			s.topics.Set(id, mapped)
		}
		mapped.version.Add(1)
		if mapped == t.topic {
			t.version++
		}
		for _, m := range recent {
			if contains(g.MessageIDs, idOf(m)) {
				at := t.records[m].receivedAt
				if at > mapped.lastAt {
					mapped.lastAt = at
				}
			}
		}
		mapped.messageIDs = append([]string{}, suffix(unique(append(mapped.messageIDs, g.MessageIDs...)), 48)...)
	}
	for s.topics.Len() > 4 {
		var removable *topic
		for _, e := range s.topics.Entries() {
			if e.Key != t.topic.id && s.tasks[e.Key] == nil && (removable == nil || e.Value.lastAt < removable.lastAt) {
				removable = e.Value
			}
		}
		if removable == nil {
			break
		}
		s.topics.Delete(removable.id)
	}
	var selectedIDs map[string]bool
	if selected != nil {
		_ = decision.Set("topicId", t.topic.id)
		selectedIDs = map[string]bool{}
		for _, id := range append(stringsOf(get(selected, "messageIds")), targets...) {
			selectedIDs[id] = true
		}
		recent = filterMessages(recent, func(m *Object) bool { return selectedIDs[idOf(m)] || selectedIDs[str(get(m, "replyToId"))] })
		t.context.setDependencies(extensionDeps, recent)
	}
	r.publishLocked(s)
	return recent, selectedIDs, nil
}
