package learningbatch

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"regexp"
)

var safeModelReasons = map[string]bool{
	"request_cancelled": true, "protocol_disabled": true, "route_not_configured": true, "invalid_tool_names": true, "all_models_failed": true, "no_compatible_model": true, "embedding_model_not_configured": true, "invalid_embedding_dimensions_setting": true, "invalid_embedding_batch_size": true, "invalid_embedding_response": true, "invalid_embedding_dimensions": true, "invalid_embedding_vector": true, "embedding_dimensions_mismatch": true, "provider_not_configured": true, "provider_timeout": true, "model_stage_timeout": true, "model_response_refused": true, "provider_request_failed": true, "invalid_provider_url": true, "credential_not_configured": true, "invalid_provider_response": true, "invalid_provider_content_type": true, "empty_provider_response": true, "provider_response_too_large": true, "invalid_provider_json": true, "protocol_not_registered": true, "protocol_operation_unsupported": true, "work_queue_full": true, "invalid_provider_usage": true, "duplicate_tool_call_id": true, "incomplete_model_response": true, "incomplete_tool_call": true, "unsupported_response_item": true, "provider_stream_interrupted": true, "provider_stream_invalid": true, "provider_stream_idle_timeout": true, "invalid_tool_arguments": true, "unknown_tool_call": true,
}
var httpFailure = regexp.MustCompile(`^provider_http_[1-5]\d\d$`)

func ModelFailureReason(err error) string {
	if e, ok := err.(*modelservice.ModelError); ok && (safeModelReasons[e.Code] || httpFailure.MatchString(e.Code)) {
		return e.Code
	}
	if err != nil && (err.Error() == "work_queue_full" || err.Error() == "request_cancelled") {
		return err.Error()
	}
	return "provider_request_failed"
}
