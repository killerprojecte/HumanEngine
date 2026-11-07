package topicroute

import (
	"container/list"
	"math"
)

// OrderedMap retains JavaScript Map insertion order. Replacing an existing key
// does not move it. The zero value is usable. Like source scene state, it is
// owned by one scene actor; callers must not access it concurrently.
type OrderedMap[K comparable, V any] struct {
	items map[K]*list.Element
	order list.List
}
type Entry[K comparable, V any] struct {
	Key   K
	Value V
}

func (m *OrderedMap[K, V]) Set(key K, value V) {
	if m.items == nil {
		m.items = make(map[K]*list.Element)
	}
	if e := m.items[key]; e != nil {
		e.Value = Entry[K, V]{key, value}
		return
	}
	m.items[key] = m.order.PushBack(Entry[K, V]{key, value})
}
func (m *OrderedMap[K, V]) Get(key K) (V, bool) {
	if e := m.items[key]; e != nil {
		return e.Value.(Entry[K, V]).Value, true
	}
	var zero V
	return zero, false
}
func (m *OrderedMap[K, V]) Delete(key K) bool {
	if e := m.items[key]; e != nil {
		m.order.Remove(e)
		delete(m.items, key)
		return true
	}
	return false
}
func (m *OrderedMap[K, V]) Len() int { return len(m.items) }
func (m *OrderedMap[K, V]) Clear()   { m.items = nil; m.order.Init() }
func (m *OrderedMap[K, V]) Entries() []Entry[K, V] {
	result := make([]Entry[K, V], 0, m.Len())
	for e := m.order.Front(); e != nil; e = e.Next() {
		result = append(result, e.Value.(Entry[K, V]))
	}
	return result
}
func (m *OrderedMap[K, V]) first() (K, bool) {
	if e := m.order.Front(); e != nil {
		return e.Value.(Entry[K, V]).Key, true
	}
	var zero K
	return zero, false
}

type Cue struct{ InitiatorID, Label string }
type Metadata struct {
	Provisional bool
	CueSourceID *string
	Cue         *Cue
}
type Deferred struct{ Schedule bool }

// RoutingState ports source scene-local metadata. It owns no message bodies,
// transport retry, turn generation or restart replay. ProtectedSends/Turns are
// intentionally untouched by Cancel, Evict and ForgetMessage, as in source.
// Do not copy a state after use: OrderedMap owns linked insertion-order nodes.
type RoutingState struct {
	Overflowed     float64
	Metadata       OrderedMap[string, *Metadata]
	Deferred       OrderedMap[string, Deferred]
	Acknowledged   OrderedMap[string, string]
	ProtectedSends OrderedMap[string, struct{}]
	Turns          OrderedMap[string, string]
}

func (s *RoutingState) Defer(id string, schedule bool) string {
	if _, ok := s.Deferred.Get(id); ok {
		return "duplicate"
	}
	// These 60/500 policies are existing source behavior, not new limits.
	if s.Deferred.Len() >= 60 {
		s.Overflowed = math.Min(9007199254740991, s.Overflowed+1)
		return "full"
	}
	s.Deferred.Set(id, Deferred{schedule})
	return "queued"
}
func (s *RoutingState) Acknowledge(messageID, topicID string) {
	s.Acknowledged.Set(messageID, topicID)
	if s.Acknowledged.Len() > 500 {
		first, _ := s.Acknowledged.first()
		s.Acknowledged.Delete(first)
	}
}
func (s *RoutingState) ForgetMessage(id string) {
	s.Deferred.Delete(id)
	for _, entry := range s.Metadata.Entries() {
		m := entry.Value
		if m.CueSourceID != nil && *m.CueSourceID == id {
			m.Cue = nil
			m.CueSourceID = nil
		}
	}
	s.Acknowledged.Delete(id)
}
func (s *RoutingState) Evict(topicID string) {
	s.Metadata.Delete(topicID)
	for _, entry := range s.Acknowledged.Entries() {
		if entry.Value == topicID {
			s.Acknowledged.Delete(entry.Key)
		}
	}
}
func (s *RoutingState) Cancel() { s.Deferred.Clear(); s.Acknowledged.Clear() }
