package providertransport

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
)

func (m *NativeModels) completeV1(ctx context.Context, a modelservice.Attempt, request CompletionRequest, messages []map[string]any) (*Completion, error) {
	if m.Host == nil {
		return nil, safe("protocol_not_registered")
	}
	protocol := a.Candidate.Provider.Protocol
	var registration *plugins.ModelProtocolMetadata
	for _, row := range m.Host.RegisteredModelProtocols() {
		if row.ID == protocol {
			v := row
			registration = &v
			break
		}
	}
	if registration == nil {
		return nil, safe("protocol_not_registered")
	}
	supported := false
	for _, op := range registration.Operations {
		if op == "complete" {
			supported = true
		}
	}
	if !supported {
		return nil, safe("protocol_operation_unsupported")
	}
	provider, ok := m.Providers[a.Candidate.Provider.ID]
	if !ok {
		return nil, safe("provider_not_configured")
	}
	_, err := providerBaseURL(provider.BaseURL)
	if err != nil {
		return nil, err
	}
	if m.Client.Credentials == nil {
		return nil, safe("credential_resolver_required")
	}
	credential, err := m.Client.Credentials.Resolve(ctx, provider)
	if err != nil {
		return nil, safe("provider_request_failed")
	}
	if credential == "" && (provider.CredentialConfigured || provider.CredentialEnv != "") {
		return nil, safe("credential_not_configured")
	}
	if m.Client.Budgets == nil {
		return nil, safe("budget_owner_required")
	}
	gate, err := m.Client.Budgets.Current(ctx)
	if err != nil {
		return nil, budgetFailure(err)
	}
	if gate != nil {
		return nil, safe("budget_protocol_unsupported")
	}
	providerSnapshot, providerOK := m.ProviderSnapshots[provider.ID]
	modelSnapshot, modelOK := m.ModelSnapshots[a.Candidate.Model.ID]
	if !providerOK || !modelOK {
		return nil, safe("protocol_configuration_snapshot_required")
	}
	callContext := map[string]any{"provider": providerSnapshot, "model": modelSnapshot}
	if credential != "" {
		callContext["credential"] = credential
	}
	visibleMessages := make([]map[string]any, len(messages))
	for i, msg := range messages {
		v := map[string]any{}
		for _, k := range []string{"role", "content", "images", "toolCalls", "toolCallId"} {
			if value, ok := msg[k]; ok {
				v[k] = value
			}
		}
		visibleMessages[i] = v
	}
	var previous *plugins.ContinuationRef
	for i := len(request.Messages) - 1; i >= 0; i-- {
		msg := request.Messages[i]
		if msg.Visible["role"] == "assistant" {
			if msg.owner == msg && msg.modelID == a.Candidate.Model.ID {
				previous = msg.continuation
			}
			break
		}
	}
	contextBytes, e := providerwire.EncodeJSON(callContext)
	if e != nil {
		return nil, safe("invalid_provider_response")
	}
	messageBytes, e := providerwire.EncodeJSON(visibleMessages)
	if e != nil {
		return nil, safe("invalid_provider_response")
	}
	toolBytes, e := providerwire.EncodeJSON(request.Tools)
	if e != nil {
		return nil, safe("invalid_provider_response")
	}
	var parsed map[string]any
	var validationError error
	result, err := m.Host.CompleteModelProtocol(ctx, protocol, registration.Generation, plugins.ModelCompletionRequest{ModelID: a.Candidate.Model.ID, Context: json.RawMessage(contextBytes), Messages: json.RawMessage(messageBytes), Tools: json.RawMessage(toolBytes), Previous: previous, Validate: func(raw json.RawMessage) error {
		parsed, validationError = parseCustom(raw, request.Tools)
		return validationError
	}})
	if err != nil {
		if errors.Is(err, plugins.ErrNotLoaded) {
			return nil, safe("protocol_disabled")
		}
		if ctx.Err() != nil {
			if cause, ok := context.Cause(ctx).(*modelservice.ModelError); ok {
				return nil, safe(cause.Code)
			}
			return nil, safe("request_cancelled")
		}
		if validationError != nil {
			return nil, safe(validationError.Error())
		}
		if classified, ok := modelservice.V1ProtocolFailure(err); ok {
			return nil, classified
		}
		return nil, safe("provider_request_failed")
	}
	text, _ := parsed["text"].(string)
	calls, _ := parsed["toolCalls"].([]any)
	usage, _ := parsed["usage"].(map[string]any)
	assistant := &Message{Visible: map[string]any{"role": "assistant", "content": text, "toolCalls": calls}, modelID: a.Candidate.Model.ID, continuation: result.Continuation()}
	assistant.owner = assistant
	return &Completion{Text: text, ToolCalls: calls, Usage: usage, ModelID: a.Candidate.Model.ID, Assistant: assistant}, nil
}
func parseCustom(raw []byte, tools []map[string]any) (map[string]any, error) {
	if len(raw) > 8*1024*1024 {
		return nil, safe("provider_response_too_large")
	}
	v, decodeErr := providerwire.DecodeObject(raw)
	if decodeErr != nil {
		return nil, safe("invalid_provider_response")
	}
	text, ok := v["text"].(string)
	if !ok {
		return nil, safe("invalid_provider_response")
	}
	ordered, decodeErr := jsjson.ParseBytes(raw)
	root, rootOK := ordered.(*jsjson.Object)
	if decodeErr != nil || !rootOK {
		return nil, safe("invalid_provider_response")
	}
	callValue, _ := root.Get("toolCalls")
	calls, ok := callValue.([]any)
	if !ok {
		return nil, safe("invalid_provider_response")
	}
	names := map[string]string{}
	for _, tool := range tools {
		if name, ok := tool["name"].(string); ok {
			names[name] = name
		}
	}
	wireCalls := []any{}
	for _, raw := range calls {
		call, ok := raw.(*jsjson.Object)
		if !ok || call == nil {
			return nil, safe("invalid_provider_response")
		}
		id, _ := call.Get("id")
		name, _ := call.Get("name")
		argumentValue, present := call.Get("arguments")
		var argumentText any
		if present {
			encoded, e := providerwire.EncodeJSON(argumentValue)
			if e != nil {
				return nil, safe("invalid_provider_response")
			}
			argumentText = string(encoded)
		}
		// Missing remains undefined at toolCall's string boundary. Validate the
		// tool name/id there first, exactly as the source completeProtocol does.
		wireCalls = append(wireCalls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": argumentText}})
	}
	result, err := providerwire.ParseChat(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": text, "tool_calls": wireCalls}}}}, names)
	if err != nil {
		return nil, err
	}
	usage, e := providerwire.NormalizedUsage(v["usage"])
	if e != nil {
		return nil, e
	}
	result["usage"] = usage
	return result, nil
}

func (m *NativeModels) customOperation(ctx context.Context, a modelservice.Attempt, operation string, input any) (any, error) {
	if m.Host == nil {
		return nil, safe("protocol_not_registered")
	}
	var registration *plugins.ModelProtocolMetadata
	for _, row := range m.Host.RegisteredModelProtocols() {
		if row.ID == a.Candidate.Provider.Protocol {
			copy := row
			registration = &copy
			break
		}
	}
	if registration == nil {
		return nil, safe("protocol_not_registered")
	}
	supported := false
	for _, op := range registration.Operations {
		if op == operation {
			supported = true
		}
	}
	if !supported {
		return nil, safe("protocol_operation_unsupported")
	}
	provider, ok := m.Providers[a.Candidate.Provider.ID]
	if !ok {
		return nil, safe("provider_not_configured")
	}
	_, e := providerBaseURL(provider.BaseURL)
	if e != nil {
		return nil, e
	}
	if m.Client.Credentials == nil {
		return nil, safe("credential_resolver_required")
	}
	secret, e := m.Client.Credentials.Resolve(ctx, provider)
	if e != nil {
		return nil, safe("provider_request_failed")
	}
	if secret == "" && (provider.CredentialConfigured || provider.CredentialEnv != "") {
		return nil, safe("credential_not_configured")
	}
	snapshot, ok := m.ProviderSnapshots[provider.ID]
	if !ok {
		return nil, safe("protocol_configuration_snapshot_required")
	}
	callContext := map[string]any{"provider": snapshot}
	if secret != "" {
		callContext["credential"] = secret
	}
	if operation != "listModels" {
		model, ok := m.ModelSnapshots[a.Candidate.Model.ID]
		if !ok {
			return nil, safe("protocol_configuration_snapshot_required")
		}
		callContext["model"] = model
		if m.Client.Budgets == nil {
			return nil, safe("budget_owner_required")
		}
		gate, e := m.Client.Budgets.Current(ctx)
		if e != nil {
			return nil, budgetFailure(e)
		}
		if gate != nil {
			return nil, safe("budget_protocol_unsupported")
		}
	}
	contextBytes, e := providerwire.EncodeJSON(callContext)
	if e != nil {
		return nil, safe("invalid_provider_response")
	}
	args := []any{json.RawMessage(contextBytes)}
	if operation != "listModels" {
		inputBytes, e := providerwire.EncodeJSON(input)
		if e != nil {
			return nil, safe("invalid_provider_response")
		}
		args = append(args, json.RawMessage(inputBytes))
	}
	raw, e := m.Host.CallModelProtocol(ctx, registration.ID, registration.Generation, operation, args)
	if e != nil {
		if errors.Is(e, plugins.ErrNotLoaded) {
			return nil, safe("protocol_disabled")
		}
		if ctx.Err() != nil {
			if cause, ok := context.Cause(ctx).(*modelservice.ModelError); ok {
				return nil, safe(cause.Code)
			}
			return nil, safe("request_cancelled")
		}
		if classified, ok := modelservice.V1ProtocolFailure(e); ok {
			return nil, classified
		}
		return nil, safe("provider_request_failed")
	}
	if operation != "listModels" && len(raw) > 8*1024*1024 {
		return nil, safe("provider_response_too_large")
	}
	value, decodeErr := providerwire.DecodeJSON(raw)
	if decodeErr != nil {
		return nil, safe("invalid_provider_response")
	}
	return value, nil
}
