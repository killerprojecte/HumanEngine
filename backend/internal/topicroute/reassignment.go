// Package topicroute owns scene-local topic routing policy, not model execution.
package topicroute

// Group contains one model-proposed grouping. IDs retain original JS string
// identity (canonical WTF-8); callers validate their own external JSON boundary.
type Group struct {
	TopicID    string
	MessageIDs []string
}
type Retained struct {
	ID         string
	MessageIDs []string
	Protected  bool
}
type Reassignment struct {
	Groups          []Group
	DeliveredIDs    []string
	CurrentTopicID  string
	SelectedTopicID *string
	Retained        []Retained
	MaxSlots        *float64
}

// ValidReassignment validates the complete grouping before any retained state
// is changed. It does not allocate IDs or certify source/ACK ownership. Missing
// delivered IDs are allowed by the source; duplicated or foreign IDs are not.
// The default four-slot policy is inherited, not a new migration guardrail.
func ValidReassignment(input Reassignment) bool {
	allowed := make(map[string]bool, len(input.DeliveredIDs))
	for _, id := range input.DeliveredIDs {
		allowed[id] = true
	}
	seen, ids := map[string]bool{}, map[string]bool{}
	selected := func(id string) bool { return input.SelectedTopicID != nil && id == *input.SelectedTopicID }
	resolved := func(id string) string {
		if selected(id) {
			return input.CurrentTopicID
		}
		return id
	}
	for _, group := range input.Groups {
		if ids[group.TopicID] {
			return false
		}
		ids[group.TopicID] = true
		for _, id := range group.MessageIDs {
			if !allowed[id] || seen[id] {
				return false
			}
			seen[id] = true
			for _, topic := range input.Retained {
				if topic.Protected && topic.ID != resolved(group.TopicID) {
					for _, message := range topic.MessageIDs {
						if message == id {
							return false
						}
					}
				}
			}
		}
	}
	resulting := map[string]bool{}
	for _, topic := range input.Retained {
		resulting[topic.ID] = true
	}
	for _, group := range input.Groups {
		id := resolved(group.TopicID)
		if id == input.CurrentTopicID && !selected(group.TopicID) {
			return false
		}
		for _, topic := range input.Retained {
			if topic.ID == id && topic.ID != input.CurrentTopicID && topic.Protected {
				return false
			}
		}
		resulting[id] = true
	}
	maxSlots := 4.0
	if input.MaxSlots != nil {
		maxSlots = *input.MaxSlots
	}
	return float64(len(resulting)) <= maxSlots
}
