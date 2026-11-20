package botruntime

import "math"

type SceneState struct {
	ActiveTasks          int        `json:"activeTasks"`
	SecondsSinceBotSpoke *float64   `json:"secondsSinceBotSpoke"`
	TopicRouting         TopicState `json:"topicRouting"`
	Topics               []Topic    `json:"topics"`
}

type TopicState struct {
	Enabled     bool    `json:"enabled"`
	Deferred    int     `json:"deferred"`
	Overflowed  float64 `json:"overflowed"`
	Provisional int     `json:"provisional"`
	MaxTopics   int     `json:"maxTopics"`
	MaxDeferred int     `json:"maxDeferred"`
}

type Topic struct {
	ID           string  `json:"id"`
	Attention    float64 `json:"attention"`
	AgeMs        float64 `json:"ageMs"`
	MessageCount int     `json:"messageCount"`
}

// SceneState reads only metadata. It does not clone message bodies or enter
// a model, transport, or global work queue.
func (r *Runtime) SceneState(id string) SceneState {
	result := SceneState{Topics: []Topic{}, TopicRouting: TopicState{
		Enabled: r.config().TopicAdmissionEnabled, MaxTopics: 4, MaxDeferred: 60,
	}}
	s := r.state(id)
	if s == nil {
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.trimLocked(s, false) {
		r.publishLocked(s)
	}
	now := r.services.Clock.Now()
	result.ActiveTasks = len(s.tasks)
	result.TopicRouting.Deferred = s.routing.Deferred.Len()
	result.TopicRouting.Overflowed = s.routing.Overflowed
	for _, e := range s.routing.Metadata.Entries() {
		if e.Value.Provisional {
			result.TopicRouting.Provisional++
		}
	}
	for i := len(s.messages) - 1; i >= 0; i-- {
		m := s.messages[i]
		if get(m, "senderId") == "bot" && get(m, "status") == "available" {
			at := s.byBody[m].receivedAt
			if !math.IsNaN(at) {
				seconds := math.Max(0, math.Floor((now-at)/1000+.5))
				result.SecondsSinceBotSpoke = &seconds
			}
			break
		}
	}
	for _, e := range s.topics.Entries() {
		t := e.Value
		age := now - t.lastAt
		count := 0
		for _, id := range t.messageIDs {
			if s.available[id] {
				count++
			}
		}
		result.Topics = append(result.Topics, Topic{t.id, t.attention * math.Exp(-age/300000), math.Max(0, age), count})
	}
	return result
}
