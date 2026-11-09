package pluginmanager

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// BuiltinPolicy is bound by application composition to actual guest operations.
// It also runs while the adapter is disabled. Native persistence only executes
// the ordered credential batch and configuration produced by that guest.
type BuiltinPolicy struct {
	CredentialFailure func(context.Context, error) error
	Validate          func(context.Context, *jsjson.Object) error
	Prepare           func(context.Context, *jsjson.Object, *jsjson.Object) (*jsjson.Object, []credentials.Change, error)
	Secrets           func(context.Context) (*jsjson.Object, error)
}

func (m *Manager) WithBuiltinPolicy(id string, policy BuiltinPolicy) error {
	if policy.Validate == nil || policy.Prepare == nil || policy.Secrets == nil {
		return errors.New("builtin_policy_incomplete")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.items[id] != nil {
		return errors.New("builtin_policy_already_registered")
	}
	if m.builtinPolicies == nil {
		m.builtinPolicies = map[string]BuiltinPolicy{}
	}
	if _, ok := m.builtinPolicies[id]; ok {
		return errors.New("builtin_policy_already_bound")
	}
	m.builtinPolicies[id] = policy
	return nil
}
