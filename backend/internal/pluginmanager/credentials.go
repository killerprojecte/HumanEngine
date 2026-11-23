package pluginmanager

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// WithCredentials borrows the same installation's native secret owner. It does
// not provision keys, bootstrap tables, or grant another plugin database access.
func (m *Manager) WithCredentials(store *credentials.Store) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if store == nil || !store.OwnedBy(m.main, m.authority) || m.credentials != nil || m.closed {
		return errors.New("plugin_credential_owner_invalid")
	}
	m.credentials = store
	return nil
}

// Guest policy owns credential names and configuration. The host owns the
// storage namespace and commits the plan and configuration together.
func (m *Manager) writeBuiltinConfiguration(ctx context.Context, id string, value, previous *jsjson.Object, policy BuiltinPolicy) error {
	config := value
	var changes []credentials.Change
	var err error
	if policy.Prepare != nil {
		config, changes, err = policy.Prepare(ctx, value, previous)
		if err != nil {
			return err
		}
	}
	raw, err := jsjson.Stringify(config)
	if err != nil {
		return err
	}
	namespace := "plugin:" + id
	apply := func(tx *recovery.MainMutation) error {
		_, err := tx.Exec("INSERT INTO main.config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", namespace, raw)
		return err
	}
	if len(changes) == 0 {
		_, err = m.authority.Write(ctx, m.main, nil, 1, apply)
	} else {
		if m.credentials == nil {
			return errors.New("plugin_credential_owner_missing")
		}
		for i := range changes {
			changes[i].Namespace = namespace
		}
		_, err = m.credentials.ApplyWith(ctx, changes, apply)
	}
	if err != nil && policy.CredentialFailure != nil {
		return policy.CredentialFailure(ctx, err)
	}
	return err
}
