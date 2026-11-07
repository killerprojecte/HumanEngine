package botruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
)

// ProviderInvocation is the required native provider/budget/accounting domain.
// The implementation authorizes the exact attempt, serializes provider messages,
// retains assistant continuation ownership and records real cost receipts. There
// is no permissive default and no TypeScript proxy.
type ProviderInvocation interface {
	Invoke(context.Context, modelservice.Attempt, ModelRequest, func(Completion, error))
}
type PolicyModels struct {
	policy   *modelservice.Policy
	provider ProviderInvocation
}

func NewPolicyModels(policy *modelservice.Policy, provider ProviderInvocation) (*PolicyModels, error) {
	if policy == nil || provider == nil {
		return nil, code("model_provider_service_required")
	}
	return &PolicyModels{policy, provider}, nil
}
func (m *PolicyModels) Start(ctx context.Context, request ModelRequest, settle func(Completion, error)) {
	expanded, err := replyplan.ExpandContextMessages(request.Messages)
	if err != nil {
		settle(Completion{}, err)
		return
	}
	request.Messages = expanded
	toolNames := []string{}
	for _, tool := range request.Tools {
		toolNames = append(toolNames, str(get(tool, "name")))
	}
	images := false
	for _, message := range expanded {
		images = images || len(array(get(message, "images"))) > 0
	}
	go func() {
		result, err := m.policy.Run(ctx, modelservice.Request{Role: request.Role, ToolNames: toolNames, HasImages: images}, func(attemptCtx context.Context, attempt modelservice.Attempt, done func(any, error)) {
			progress := attempt.FirstContent
			attempt.FirstContent = func() {
				progress()
				if request.FirstContent != nil {
					request.FirstContent()
				}
			}
			m.provider.Invoke(attemptCtx, attempt, request, func(c Completion, e error) { done(c, e) })
		})
		if err != nil {
			settle(Completion{}, err)
			return
		}
		completion, ok := result.(Completion)
		if !ok {
			settle(Completion{}, code("model_completion_invalid"))
			return
		}
		settle(completion, nil)
	}()
}
