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

// credentialPreparation follows the builtin prepareConfiguration transformations.
// The returned changes must commit atomically with the returned configuration.
func credentialPreparation(id string, input, previous *jsjson.Object) (*jsjson.Object, []credentials.Change) {
	config := clone(input)
	type field struct{ secret, clear, legacy string }
	fields := []field{}
	switch id {
	case "web-search":
		fields = []field{{"tavilyKey", "clearTavilyKey", ""}, {"youKey", "clearYouKey", ""}, {"deepseekKey", "clearDeepseekKey", ""}}
	case "snowluma":
		if prop(config, "transport") != "http" {
			for _, key := range []string{"eventHost", "eventPort", "eventPath", "eventCredentialEnv"} {
				if value, ok := previous.Get(key); ok {
					_ = config.Set(key, value)
				} else {
					config.Delete(key)
				}
			}
			config.Delete("eventToken")
			config.Delete("clearEventToken")
		}
		fields = []field{{"token", "clearToken", "credentialEnv"}, {"eventToken", "clearEventToken", "eventCredentialEnv"}}
	}
	changes := []credentials.Change{}
	for _, f := range fields {
		var mutation *credentials.Mutation
		if prop(config, f.clear) == true {
			mutation = &credentials.Mutation{Action: "clear"}
			if f.legacy != "" {
				config.Delete(f.legacy)
			}
		} else if value, ok := prop(config, f.secret).(string); ok && value != "" {
			mutation = &credentials.Mutation{Action: "set", Value: value}
		}
		if mutation != nil {
			changes = append(changes, credentials.Change{Namespace: "plugin:" + id, Name: f.secret, Mutation: mutation})
		}
		config.Delete(f.secret)
		config.Delete(f.clear)
	}
	return config, changes
}

func (m *Manager) writeBuiltinConfiguration(ctx context.Context, id string, value, previous *jsjson.Object) error {
	config, changes := credentialPreparation(id, value, previous)
	raw, err := jsjson.Stringify(config)
	if err != nil {
		return err
	}
	apply := func(tx *recovery.MainMutation) error {
		_, err := tx.Exec("INSERT INTO main.config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "plugin:"+id, raw)
		return err
	}
	if id == "web-search" || id == "snowluma" {
		if m.credentials == nil {
			return errors.New("plugin_credential_owner_missing")
		}
		_, err = m.credentials.ApplyWith(ctx, changes, apply)
	} else {
		_, err = m.authority.Write(ctx, m.main, nil, 1, apply)
	}
	return err
}
