package assertions

// ValidHistoryInstant is queryAssertionHistory's finite, canonical ISO string
// check. Unlike nullable revision timestamps, explicit null is not an instant.
func ValidHistoryInstant(value any) bool { return value != nil && validTime(value) }
