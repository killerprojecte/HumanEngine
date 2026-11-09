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
	if id != "typing-delay" {
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
func (m *Manager) inspectBuiltin(i *item, operation string, input, previous *jsjson.Object, requestID string) (plugins.Inspection, error) {
	manifest, _ := jsjson.Marshal(i.manifest)
	result := plugins.Inspection{Manifest: manifest, ConfiguredSecrets: json.RawMessage(`{}`)}
	if operation == "secrets" {
		return result, nil
	}
	value, _ := pluginconfig.Preserve(input, true, previous, true, schema(i))
	if err := pluginconfig.ValidateBuiltin(value, schema(i)); err != nil {
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
	if err = pluginconfig.ValidateBuiltin(value, schema(i)); err != nil {
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
		return errors.New("builtin_runtime_not_implemented")
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
