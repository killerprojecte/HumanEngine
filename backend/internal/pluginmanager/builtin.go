package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginconfig"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

// WithWASM borrows the application's one WASM host before registration/serving.
func (m *Manager) WithWASM(host *plugins.WASMHost) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if host == nil || m.closed || m.wasm != nil {
		return errors.New("plugin_wasm_owner_invalid")
	}
	m.wasm = host
	return nil
}

// BuiltinDescriptor supplies distribution-owned resources and host bindings.
type BuiltinDescriptor struct {
	Manifest     json.RawMessage
	LoadArtifact func() ([]byte, error)
	Start        BuiltinRuntimeFactory
	Policy       BuiltinPolicy
}

// RegisterBuiltin admits a composed V2 guest without opening its artifact.
func (m *Manager) RegisterBuiltin(descriptor BuiltinDescriptor) error {
	return m.registerWASM(descriptor, true)
}

func (m *Manager) registerWASM(descriptor BuiltinDescriptor, builtin bool) error {
	parsed, err := jsjson.Parse(string(descriptor.Manifest))
	if err != nil {
		return err
	}
	manifest, ok := parsed.(*jsjson.Object)
	if !ok || prop(manifest, "apiVersion") != float64(2) {
		return errors.New("builtin_manifest_invalid")
	}
	id := text(manifest, "id")
	if !validID(id) {
		return errors.New("invalid_plugin_id")
	}
	if descriptor.LoadArtifact == nil || descriptor.Start == nil {
		return errors.New("builtin_runtime_not_implemented")
	}
	policy := descriptor.Policy
	if (policy.Configure != nil || policy.CredentialFailure != nil || policy.Validate != nil || policy.Prepare != nil || policy.Secrets != nil) &&
		(policy.Validate == nil || policy.Prepare == nil || policy.Secrets == nil) {
		return errors.New("builtin_policy_incomplete")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.wasm == nil {
		return errors.New("plugin_wasm_owner_invalid")
	}
	if m.items[id] != nil {
		return errors.New("invalid_plugin_id")
	}
	tail := make(chan struct{})
	close(tail)
	life, cancel := context.WithCancel(context.Background())
	m.items[id] = &item{builtin: builtin, manifest: manifest, artifact: descriptor.LoadArtifact, factory: descriptor.Start, policy: policy, tail: tail, ctx: life, cancel: cancel, pages: []any{}, actions: map[string]string{}}
	m.order = append(m.order, id)
	return nil
}

func inspectBuiltinPolicy(ctx context.Context, i *item, policy BuiltinPolicy, operation string, input, previous *jsjson.Object, requestID string) (plugins.Inspection, error) {
	manifest, _ := jsjson.Marshal(i.manifest)
	result := plugins.Inspection{Manifest: manifest, ConfiguredSecrets: json.RawMessage(`{}`)}
	bound := policy.Validate != nil
	if operation == "secrets" {
		if bound {
			secrets, e := policy.Secrets(ctx)
			if e != nil {
				return result, e
			}
			result.ConfiguredSecrets, _ = jsjson.Marshal(secrets)
		}
		return result, nil
	}
	value, _ := pluginconfig.Preserve(input, true, previous, true, schema(i))
	if bound {
		object, ok := value.(*jsjson.Object)
		if !ok {
			return result, &ConfigurationError{RequestID: requestID, Issues: []Issue{}}
		}
		if e := policy.Validate(ctx, object); e != nil {
			return result, e
		}
	} else if err := pluginconfig.ValidateBuiltin(value, schema(i)); err != nil {
		return result, &ConfigurationError{RequestID: requestID, Issues: []Issue{}}
	}
	result.Config, _ = jsjson.Marshal(value)
	safe, _ := pluginconfig.Redact(value, schema(i))
	result.Redacted, _ = jsjson.Marshal(safe)
	return result, nil
}
func (m *Manager) startBuiltin(ctx context.Context, id string, i *item, revision uint64, config json.RawMessage) error {
	value, err := jsjson.Parse(string(config))
	if err != nil {
		return err
	}
	policy := i.policy
	if policy.Validate != nil {
		object, ok := value.(*jsjson.Object)
		if !ok {
			return &ConfigurationError{Issues: []Issue{}}
		}
		if err = policy.Validate(ctx, object); err != nil {
			return err
		}
	} else if err = pluginconfig.ValidateBuiltin(value, schema(i)); err != nil {
		return &ConfigurationError{Issues: []Issue{}}
	}
	artifact, err := i.artifact()
	if err != nil {
		return err
	}
	if err = m.wasm.Enable(ctx, id, artifact); err != nil {
		return err
	}
	callGuest, err := m.wasm.BindCall(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	life := i.ctx
	m.mu.Unlock()
	return m.startBoundBuiltin(ctx, id, i, revision, config, life, callGuest)
}
