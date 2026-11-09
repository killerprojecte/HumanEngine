package botruntime

import "github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

// toolCompletionOutcome mirrors diagnostics/runtime-results.ts. Only closed
// status labels, result counts and controlled reason codes leave this boundary.
func toolCompletionOutcome(output any) Event {
	result := Event{Status: "completed", Reason: "tool_returned", Counts: map[string]int{"results": 0}}
	values, isArray := output.([]any)
	if isArray {
		result.Counts["results"] = len(values)
	} else if items, ok := get(output, "items").([]any); ok {
		result.Counts["results"] = len(items)
	} else if items, ok := get(output, "results").([]any); ok {
		result.Counts["results"] = len(items)
	}
	segments := isArray && len(values) > 0
	for _, v := range values {
		if !contains([]string{"sent", "failed", "unknown", "cancelled"}, str(get(v, "status"))) {
			segments = false
			break
		}
	}
	if segments {
		for _, state := range []string{"sent", "failed", "unknown", "cancelled"} {
			result.Counts[state+"Segments"] = 0
			for _, v := range values {
				if get(v, "status") == state {
					result.Counts[state+"Segments"]++
				}
			}
		}
		sent := result.Counts["sentSegments"] > 0
		if result.Counts["unknownSegments"] > 0 || result.Counts["failedSegments"] > 0 {
			result.Status = "failed"
			if sent {
				result.Status = "partial"
			}
			result.Reason = "delivery_failed"
			if result.Counts["unknownSegments"] > 0 {
				result.Reason = "delivery_unknown"
			}
			return result
		}
		if result.Counts["cancelledSegments"] > 0 {
			result.Status = "cancelled"
			if sent {
				result.Status = "partial"
			}
			result.Reason = "send_cancelled"
			return result
		}
		result.Reason = "segments_delivered"
		return result
	}
	status := get(output, "status")
	if status == "cancelled" {
		result.Status = "cancelled"
		result.Reason = "context_cancelled"
		return result
	}
	hasError := false
	if o, ok := output.(*Object); ok {
		if value, present := o.Get("error"); present && !jsjson.IsUndefined(value) {
			hasError = true
		}
	}
	if status == "rejected" || status == "unavailable" || status == "failed" || hasError || get(output, "ok") == false {
		result.Status = "failed"
		result.Reason = "tool_rejected"
		return result
	}
	if status == "queued" || status == "duplicate" {
		result.ReceiptState = str(status)
	}
	if status == "no_results" {
		result.Reason = "query_no_results"
	}
	return result
}
