package pluginmanager

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

// BuiltinLifetime separates logical revocation from physical teardown. Close
// must be idempotent and must not return success while separately owned native
// work still owns that generation's callbacks. The manager owns BeginWork.
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
	BeginWork func() (func(), bool)
	JoinLife  func(context.Context, context.Context) (context.Context, func())
}
type BuiltinRuntimeFactory func(context.Context, BuiltinRuntimeInput) (BuiltinLifetime, error)

func (m *Manager) startBoundBuiltin(ctx context.Context, id string, i *item, revision uint64, config json.RawMessage, life context.Context, call func(context.Context, []byte) ([]byte, error)) error {
	m.mu.Lock()
	credentials := m.credentials
	m.mu.Unlock()
	owner, err := i.factory(ctx, BuiltinRuntimeInput{ID: id, Revision: revision, Config: config, Life: life, Current: func() bool { return current(m, i, revision) }, Host: m.wasm, CallGuest: call, BeginWork: m.work.Start, JoinLife: lifetime, Secret: func(ctx context.Context, name string) (string, bool, error) {
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
