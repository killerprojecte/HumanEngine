package topicroute

import "testing"

func TestReviewSelectedProtectedBoundaries(t *testing.T) {
	selected := "proposal"
	for _, tc := range []struct {
		name     string
		groups   []Group
		retained []Retained
		want     bool
	}{
		{"selected replaces protected current", []Group{{"proposal", []string{"a"}}}, []Retained{{"current", []string{"a"}, true}}, true},
		{"selected cannot steal protected proposal", []Group{{"proposal", []string{"a"}}}, []Retained{{"proposal", []string{"a"}, true}}, false},
		{"empty protected noncurrent still rejected", []Group{{"old", nil}}, []Retained{{"old", nil, true}}, false},
		{"selected and explicit current collide", []Group{{"proposal", nil}, {"current", nil}}, nil, false},
		{"missing delivered protected message allowed", nil, []Retained{{"old", []string{"a"}, true}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := Reassignment{Groups: tc.groups, DeliveredIDs: []string{"a", "a"}, CurrentTopicID: "current", SelectedTopicID: &selected, Retained: tc.retained}
			if got := ValidReassignment(input); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestReviewMetadataAliasingAndCancelScope(t *testing.T) {
	var s RoutingState
	id := "source"
	m := &Metadata{Provisional: true, CueSourceID: &id, Cue: &Cue{"sender", "label"}}
	s.Metadata.Set("first", m)
	s.Metadata.Set("second", m)
	s.ProtectedSends.Set("first", struct{}{})
	s.Turns.Set("first", "turn")
	s.Defer(id, true)
	s.Acknowledge(id, "first")
	s.ForgetMessage(id)
	if m.Cue != nil || m.CueSourceID != nil || !m.Provisional {
		t.Fatal("forget must mutate the shared metadata object only at cue fields")
	}
	s.Defer("pending", false)
	s.Acknowledge("sent", "first")
	s.Overflowed = 9
	s.Cancel()
	if s.Deferred.Len() != 0 || s.Acknowledged.Len() != 0 || s.Metadata.Len() != 2 || s.ProtectedSends.Len() != 1 || s.Turns.Len() != 1 || s.Overflowed != 9 {
		t.Fatal("cancel changed state beyond its source scope")
	}
	s.Evict("first")
	if v, ok := s.Metadata.Get("second"); !ok || v != m || s.ProtectedSends.Len() != 1 || s.Turns.Len() != 1 {
		t.Fatal("evict removed shared metadata or protected/turn ownership")
	}
}

func TestReviewAcknowledgementDeletesOnlyOneWhenAlreadyOverCapacity(t *testing.T) {
	var s RoutingState
	for i := 0; i < 503; i++ {
		s.Acknowledged.Set(string(rune(i)), "topic")
	}
	s.Acknowledge(string(rune(400)), "replacement")
	if s.Acknowledged.Len() != 502 {
		t.Fatal("source removes exactly one oldest entry, not all excess entries")
	}
	if _, ok := s.Acknowledged.Get(string(rune(0))); ok {
		t.Fatal("oldest entry retained")
	}
	if first, _ := s.Acknowledged.first(); first != string(rune(1)) {
		t.Fatal("replacement changed insertion order")
	}
}
