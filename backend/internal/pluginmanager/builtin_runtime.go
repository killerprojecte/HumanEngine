package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/plugins/metadata"
)

// BuiltinLifetime separates logical revocation from physical teardown. Close
// must be idempotent and must not return success while native IO still owns the
// generation's callbacks. The manager drains it before unloading the guest.
type BuiltinLifetime interface {
	Dispose()
	CloseAndWait(context.Context) error
}
type BuiltinRuntimeInput struct {
	ID        string
	Revision  uint64
	Config    json.RawMessage
	Life      context.Context
	Current   func() bool
	Host      *plugins.WASMHost
	CallGuest func(context.Context, []byte) ([]byte, error)
	Secret    func(context.Context, string) (string, bool, error)
}
type BuiltinRuntimeFactory func(context.Context, BuiltinRuntimeInput) (BuiltinLifetime, error)

// WithBuiltinRuntime is application composition, not a plugin capability API.
// Factories live beneath their plugin aggregate directory. Registration is not
// enabled until that application's explicit factory has been bound exactly once.
func (m *Manager) WithBuiltinRuntime(id string, factory BuiltinRuntimeFactory) error {
	if factory == nil || id == "typing-delay" {
		return errors.New("builtin_runtime_binding_invalid")
	}
	if _, ok := metadata.Manifest(id).(json.RawMessage); !ok {
		return errors.New("builtin_manifest_invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.items[id] != nil || m.builtinFactories[id] != nil {
		return errors.New("builtin_runtime_already_bound")
	}
	if m.builtinFactories == nil {
		m.builtinFactories = map[string]BuiltinRuntimeFactory{}
	}
	m.builtinFactories[id] = factory
	return nil
}
func (m *Manager) startBoundBuiltin(ctx context.Context, id string, i *item, revision uint64, config json.RawMessage, life context.Context, call func(context.Context, []byte) ([]byte, error)) error {
	m.mu.Lock()
	factory := m.builtinFactories[id]
	credentials := m.credentials
	m.mu.Unlock()
	if factory == nil {
		return errors.New("builtin_runtime_not_implemented")
	}
	owner, err := factory(ctx, BuiltinRuntimeInput{ID: id, Revision: revision, Config: config, Life: life, Current: func() bool { return current(m, i, revision) }, Host: m.wasm, CallGuest: call, Secret: func(ctx context.Context, name string) (string, bool, error) {
		if credentials == nil {
			return "", false, errors.New("plugin_credential_owner_missing")
		}
		return credentials.Get(ctx, "plugin:"+id, name)
	}})
	// A factory may return an owner alongside an error when setup already owns
	// physical resources. Retain it even on failure so cleanup cannot leak them.
	if owner != nil {
		m.mu.Lock()
		i.cleanups = append(i.cleanups, owner.Dispose)
		i.physicalCleanups = append(i.physicalCleanups, owner.CloseAndWait)
		m.mu.Unlock()
	}
	if err == nil && owner == nil {
		return errors.New("builtin_runtime_owner_missing")
	}
	return err
}
