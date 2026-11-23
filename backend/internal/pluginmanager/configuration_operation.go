package pluginmanager

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

type configurationSession struct {
	builtinPolicy BuiltinPolicy
	validate      func(*jsjson.Object) error
	prepare       func() (any, error)
	operation     *plugins.ConfigurationOperation
	close         func()
}

// beginConfiguration scopes the policy across source phases. V1 retains one
// module; an opted-in builtin retains only code, not guest state. Only the
// second validator's newly preserved object is passed to prepare.
func (m *Manager) beginConfiguration(ctx context.Context, id string, i *item, input, previous *jsjson.Object, requestID string) (*configurationSession, error) {
	if i.builtin {
		policy := i.policy
		closePolicy := func() {}
		if policy.Configure != nil {
			policy, closePolicy = policy.Configure()
		}
		if _, err := inspectBuiltinPolicy(ctx, i, policy, "validate", input, previous, requestID); err != nil {
			closePolicy()
			return nil, err
		}
		var value any
		return &configurationSession{builtinPolicy: policy, validate: func(previous *jsjson.Object) error {
			result, err := inspectBuiltinPolicy(ctx, i, policy, "validate", input, previous, requestID)
			if err != nil {
				return err
			}
			value, err = jsjson.Parse(string(result.Config))
			return err
		}, prepare: func() (any, error) { return value, nil }, close: closePolicy}, nil
	}
	raw, err := jsjson.Marshal(input)
	if err != nil {
		return nil, err
	}
	old, err := jsjson.Marshal(previous)
	if err != nil {
		return nil, err
	}
	request := plugins.InspectionRequest{ID: id, Path: i.path, Config: raw, Previous: old}
	m.mu.Lock()
	enabled, revision := i.desired && m.host.HasCurrentModule(id), i.revision
	m.mu.Unlock()
	var op *plugins.ConfigurationOperation
	var result plugins.Inspection
	if enabled {
		op, result, err = m.host.BeginCurrentConfiguration(ctx, id, request)
	} else {
		op, result, err = m.host.BeginManagedConfiguration(ctx, id, request)
	}
	if err != nil && ctx.Err() != nil && enabled {
		m.retireNativeGeneration(id, i, revision)
	}
	if err == nil && result.Failed {
		err = inspectError(result, requestID)
	}
	if err != nil {
		return nil, err
	}
	return &configurationSession{operation: op, validate: func(previous *jsjson.Object) error {
		raw, err := jsjson.Marshal(previous)
		if err != nil {
			return err
		}
		result, err := op.Validate(raw)
		if err == nil && result.Failed {
			err = inspectError(result, requestID)
		}
		return err
	}, prepare: func() (any, error) {
		result, err := op.Prepare()
		if err == nil && result.Failed {
			err = inspectError(result, requestID)
		}
		if err != nil {
			return nil, err
		}
		return jsjson.Parse(string(result.Config))
	}, close: op.Close}, nil
}

// The host already retired the exact blocked isolate. Remove only that manager
// generation's advertised capabilities; never disable a replacement by ID.
func (m *Manager) retireNativeGeneration(id string, i *item, revision uint64) {
	m.mu.Lock()
	if i.revision != revision {
		m.mu.Unlock()
		return
	}
	i.cancel()
	i.enabled = false
	i.hostGeneration = 0
	cleanups := i.cleanups
	i.cleanups = nil
	i.pages = []any{}
	i.actions = map[string]string{}
	m.mu.Unlock()
	m.cache.InvalidatePlugin(id)
	for n := len(cleanups) - 1; n >= 0; n-- {
		cleanups[n]()
	}
}
