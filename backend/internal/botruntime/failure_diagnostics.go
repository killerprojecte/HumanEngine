package botruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// Do not return arbitrary Error() text, SQL, model output, panic payloads or
// provider bodies. The original error remains available to the trusted caller.
func runtimeFailureCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "request_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, jsjson.ErrUnsupportedType) {
		return "json_value_type_unsupported"
	}
	if errors.Is(err, jsjson.ErrStringEncoding) {
		return "json_string_encoding_invalid"
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		switch current.Error() {
		case "memory_read_revoked", "personalization_disabled", "planner_context_ineligible", "planner_forbidden_tool", "planner_missing_plan", "planner_tool_budget_exhausted", "memory_tool_arguments_invalid", "identity_tool_arguments_invalid", "runtime_unknown_tool", "tool_unavailable", "runtime_closed", "turn_finished", "model_response_object_required", "identity_receipt_not_delivered", "recovery_quarantined":
			return current.Error()
		}
	}
	return "runtime_operation_failed"
}
