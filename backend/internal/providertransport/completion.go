package providertransport

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"reflect"

	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
)

// Message owns native Responses continuation by exact object identity. Copying
// visible fields or copying this struct does not grant continuation ownership.
// V1 opaque continuations remain owned by the plugin host, not this struct.
type Message struct {
	continuation *plugins.ContinuationRef

	Visible map[string]any
	owner   *Message
	modelID string
	output  []any
}
type Completion struct {
	Text      string
	ToolCalls []any
	Usage     map[string]any
	ModelID   string
	Assistant *Message
	Summary   map[string]any
}
type CompletionRequest struct {
	FirstContent func()
	Role         string
	Messages     []*Message
	Tools        []map[string]any
}
type NativeModels struct {
	Diagnostics       DiagnosticSink
	requests          quiescence.Group
	attempts          quiescence.Group
	Host              *plugins.V1Host
	ProviderSnapshots map[string]map[string]any
	ModelSnapshots    map[string]map[string]any

	Policy     *modelservice.Policy
	Client     *Client
	Providers  map[string]Provider
	Prices     map[string]map[string]float64
	CacheModes map[string]string
}

func (m *NativeModels) Complete(ctx context.Context, request CompletionRequest) (*Completion, error) {
	doneWork, ok := m.requests.Start()
	if !ok {
		return nil, safe("native_models_closed")
	}
	defer doneWork()
	if ctx == nil {
		ctx = context.Background()
	}
	if e := checkModelRead(ctx); e != nil {
		return nil, e
	}
	if m.Policy == nil || m.Client == nil {
		return nil, errors.New("native_provider_composition_required")
	}
	messages := make([]map[string]any, len(request.Messages))
	for i, msg := range request.Messages {
		if msg == nil {
			return nil, safe("invalid_provider_response")
		}
		messages[i] = msg.Visible
	}
	expanded, e := providerwire.ExpandContextMessages(messages)
	if e != nil {
		return nil, safe(e.Error())
	}
	names := make([]string, len(request.Tools))
	wireNames := map[string]string{}
	for i, tool := range request.Tools {
		name, ok := tool["name"].(string)
		if !ok {
			return nil, safe("invalid_tool_name")
		}
		names[i] = name
		wireNames[providerwire.WireName(name)] = name
	}
	hasImages := false
	for _, message := range expanded {
		if images, ok := message["images"].([]any); ok && len(images) > 0 {
			hasImages = true
		}
	}
	ctx, e = WithBudgetRequest(ctx)
	if e != nil {
		return nil, safe("provider_request_failed")
	}
	value, e := m.Policy.Run(ctx, modelservice.Request{Role: request.Role, ToolNames: names, HasImages: hasImages}, m.asyncInvocation(func(ctx context.Context, attempt modelservice.Attempt, complete func(any, error)) {
		if request.FirstContent != nil {
			first := attempt.FirstContent
			attempt.FirstContent = func() { first(); request.FirstContent() }
		}
		if err := checkModelRead(ctx); err != nil {
			complete(nil, err)
			return
		}
		if !modelservice.IsBuiltin(attempt.Candidate.Provider.Protocol) {
			value, err := m.completeV1(ctx, attempt, request, expanded)
			complete(value, err)
			return
		}
		model := attempt.Candidate.Model
		provider, ok := m.Providers[model.ProviderID]
		if !ok {
			complete(nil, safe("provider_not_configured"))
			return
		}
		if provider.Protocol != attempt.Candidate.Provider.Protocol {
			complete(nil, safe("provider_configuration_changed"))
			return
		}
		cache := attempt.Candidate.Role == "planner" && providerwire.SupportsOpenAIPromptBreakpoints(provider.Protocol, provider.BaseURL, model.APIModelName, m.CacheModes[model.ID])
		hasBreakpoint := false
		for _, msg := range expanded {
			if msg["promptCacheBreakpoint"] == true {
				hasBreakpoint = true
			}
		}
		cache = cache && hasBreakpoint
		stream := model.Streaming == nil || *model.Streaming
		body, err := providerwire.BuildRequest(provider.Protocol, model.APIModelName, expanded, request.Tools, cache, stream)
		if err != nil {
			complete(nil, safe(err.Error()))
			return
		}
		if provider.Protocol == "openai-responses" {
			owners := map[uintptr]*Message{}
			for _, message := range request.Messages {
				owners[uintptr(reflect.ValueOf(message.Visible).UnsafePointer())] = message
			}
			input := []any{}
			for _, row := range expanded {
				owner := owners[uintptr(reflect.ValueOf(row).UnsafePointer())]
				if row["role"] != "tool" && owner != nil && owner.owner == owner && owner.modelID == model.ID {
					input = append(input, owner.output...)
					continue
				}
				encoded, err := providerwire.ResponsesInput([]map[string]any{row}, cache)
				if err != nil {
					complete(nil, safe(err.Error()))
					return
				}
				for _, v := range encoded {
					input = append(input, v)
				}
			}
			body["input"] = input
		}

		path := "chat/completions"
		if provider.Protocol == "openai-responses" {
			path = "responses"
		}
		descriptor := Descriptor{ProviderID: provider.ID, ModelID: model.ID, Protocol: provider.Protocol, APIModelName: model.APIModelName, Operation: "complete", Role: attempt.Candidate.Role, Prices: m.Prices[model.ID]}
		m.nativeCompletion(Request{Provider: provider, Path: path, Body: body, Descriptor: &descriptor, diagnostic: func(ctx context.Context, c *Completion, secrets []string) {
			m.captureDiagnostic(ctx, attempt.Candidate.Role, expanded, c, false, secrets)
		}}, model.ID, wireNames)(ctx, attempt, complete)

	}))
	if e != nil {
		return nil, e
	}
	result, ok := value.(*Completion)
	if !ok {
		return nil, safe("invalid_provider_response")
	}
	return result, nil
}

// Release is explicit when the assistant leaves its owning conversation.
func (m *Message) Release() error {
	if m == nil || m.owner != m {
		return nil
	}
	if m.continuation != nil {
		return m.continuation.Release()
	}
	return nil
}

func (m *NativeModels) nativeCompletion(r Request, modelID string, wireNames map[string]string) modelservice.Invocation {
	return m.asyncInvocation(func(ctx context.Context, attempt modelservice.Attempt, complete func(any, error)) {
		r.FirstContent = attempt.FirstContent
		secrets := append([]string{}, r.diagnosticSecrets...)
		r.credentialUsed = func(value string) {
			if value != "" {
				secrets = append(secrets, value)
			}
		}
		result, err := m.Client.Request(ctx, r)
		if retry, ok := err.(*imageRetry); ok {
			retry.request.diagnosticSecrets = secrets
			complete(nil, modelservice.RescheduleNativeImage(m.nativeCompletion(retry.request, modelID, wireNames)))
			return
		}
		if err != nil {
			complete(nil, err)
			return
		}

		var parsed map[string]any
		if r.Provider.Protocol == "openai-responses" {
			parsed, err = providerwire.ParseResponses(result, wireNames)
		} else {
			parsed, err = providerwire.ParseChat(result, wireNames)
		}
		if err != nil {
			complete(nil, safe(err.Error()))
			return
		}
		text, _ := parsed["text"].(string)
		calls, _ := parsed["toolCalls"].([]any)
		usage, _ := parsed["usage"].(map[string]any)
		summary, _ := parsed["responseSummary"].(map[string]any)
		assistant := &Message{Visible: map[string]any{"role": "assistant", "content": text, "toolCalls": calls}}
		if r.Provider.Protocol == "openai-responses" {
			assistant.owner = assistant
			assistant.modelID = modelID
			assistant.output, _ = result["output"].([]any)
		}
		completion := &Completion{text, calls, usage, modelID, assistant, summary}
		if r.diagnostic != nil {
			r.diagnostic(ctx, completion, secrets)
		}
		complete(completion, nil)
	})
}

// Scheduler starters must return promptly; network and isolate work settle only
// when their actual operation ends. Panics cannot strand an admitted slot.
func asyncInvocation(invoke modelservice.Invocation) modelservice.Invocation {
	return func(ctx context.Context, a modelservice.Attempt, done func(any, error)) {
		go func() {
			defer func() {
				if recover() != nil {
					done(nil, safe("provider_request_failed"))
				}
			}()
			invoke(ctx, a, done)
		}()
	}
}

func (c *Completion) Release() error {
	if c == nil {
		return nil
	}
	return c.Assistant.Release()
}
