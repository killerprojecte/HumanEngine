package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginconfig"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/plugins/metadata"
	typinghost "github.com/killerprojecte/HumanEngine/backend/plugins/typing-delay/host"
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

// RegisterBuiltin admits only guests with a native runtime composition.
// Other policy guests are not presented as working runtime plugins.
func (m *Manager) RegisterBuiltin(id string) error {
	m.mu.Lock()
	bound := m.builtinFactories[id] != nil
	m.mu.Unlock()
	if id != "typing-delay" && !bound {
		return errors.New("builtin_runtime_not_implemented")
	}
	raw, ok := metadata.Manifest(id).(json.RawMessage)
	if !ok {
		return errors.New("builtin_manifest_invalid")
	}
	parsed, err := jsjson.Parse(string(raw))
	if err != nil {
		return err
	}
	manifest := parsed.(*jsjson.Object)
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
	m.items[id] = &item{builtin: true, manifest: manifest, tail: tail, ctx: life, cancel: cancel, pages: []any{}, actions: map[string]string{}}
	m.order = append(m.order, id)
	return nil
}
func (m *Manager) inspectBuiltin(ctx context.Context, i *item, operation string, input, previous *jsjson.Object, requestID string) (plugins.Inspection, error) {
	manifest, _ := jsjson.Marshal(i.manifest)
	result := plugins.Inspection{Manifest: manifest, ConfiguredSecrets: json.RawMessage(`{}`)}
	id, _ := i.manifest.Get("id")
	m.mu.Lock()
	policy, bound := m.builtinPolicies[id.(string)]
	m.mu.Unlock()
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
	m.mu.Lock()
	policy, bound := m.builtinPolicies[id]
	m.mu.Unlock()
	if bound {
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
	artifact, err := plugins.BuiltinArtifact(id)
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
	if id != "typing-delay" {
		return m.startBoundBuiltin(ctx, id, i, revision, config, life, callGuest)
	}
	remove, err := m.runtime.InstallTypingDelay(typinghost.Delay(typinghost.Ports{Life: life, Config: config, BeginWork: m.work.Start, Current: func() bool { return current(m, i, revision) }, Lifetime: lifetime, CallGuest: callGuest}))
	if err != nil {
		return err
	}
	m.mu.Lock()
	i.cleanups = append(i.cleanups, remove)
	m.mu.Unlock()
	return nil
}
