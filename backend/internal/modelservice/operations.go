package modelservice

import "context"

// RunProviderOperation executes an explicitly selected provider operation. Unlike
// completion routing, embeddings do not require a text capability and model lists
// do not require a model. Native model listing bypasses the model scheduler, as
// the source does. Custom listing uses the shared scheduler and cancellation race.
func (p *Policy) RunProviderOperation(ctx context.Context, providerID, modelID, role string, invoke Invocation) (any, error) {
	doneWork, ok := p.work.Start()
	if !ok {
		return nil, NewError("model_policy_closed", nil, false)
	}
	defer doneWork()
	if ctx == nil {
		ctx = context.Background()
	}
	if invoke == nil {
		return nil, NewError("model_invocation_required", nil, false)
	}
	var provider *Provider
	for _, v := range p.config.Providers {
		if v.ID == providerID {
			copy := cloneProvider(v)
			provider = &copy
			break
		}
	}
	if provider == nil {
		return nil, NewError("provider_not_configured", nil, false)
	}
	model := Model{}
	if role != "models" {
		found := false
		for _, v := range p.config.Models {
			if v.ID == modelID {
				model = cloneModel(v)
				found = true
				break
			}
		}
		if !found || !contains(model.Capabilities, "embedding") {
			return nil, NewError("embedding_model_not_configured", nil, false)
		}
	}
	candidate := Candidate{Role: role, Model: model, Provider: *provider}
	if role != "models" || !IsBuiltin(provider.Protocol) {
		return p.attempts(ctx, candidate, invoke)
	}
	policy, err := ProviderPolicy(*provider)
	if err != nil {
		return nil, err
	}
	for number := 1; number <= policy.MaxAttempts; number++ {
		if ctx.Err() != nil {
			return nil, cancelled(context.Cause(ctx))
		}
		deadline := newDeadline(ctx, policy.TimeoutMS, "provider_timeout", p.clock)
		done := make(chan outcome, 1)
		invoke(deadline.Context, Attempt{Candidate: candidate, Number: number, MaxAttempts: policy.MaxAttempts, TimeoutMS: policy.TimeoutMS, FirstContent: func() {}}, func(v any, e error) { done <- outcome{v, e} })
		result := <-done
		deadline.Close()
		if ctx.Err() != nil {
			return nil, cancelled(context.Cause(ctx))
		}
		if result.err == nil {
			return result.value, nil
		}
		failure, ok := result.err.(*ModelError)
		if number == policy.MaxAttempts || !ok || !failure.Retryable {
			return nil, markHandled(result.err)
		}
		if err := p.sleep(ctx, RetryDelay(float64(number), failure.RetryAfterMS)); err != nil {
			return nil, err
		}
	}
	return nil, NewError("invalid_provider_request_policy", nil, false)
}

type operationPriorityKey struct{}

// WithProviderOperationPriority carries the trusted budget-work lane for explicit
// embeddings operations. Scheduler background scopes still only demote it.
func WithProviderOperationPriority(ctx context.Context, priority float64) context.Context {
	return context.WithValue(ctx, operationPriorityKey{}, priority)
}
func operationPriority(ctx context.Context, role string) float64 {
	if role == "embedding" {
		if v, ok := ctx.Value(operationPriorityKey{}).(float64); ok && (v == 0 || v == 1 || v == 2) {
			return v
		}
	}
	return BasePriority(role)
}
