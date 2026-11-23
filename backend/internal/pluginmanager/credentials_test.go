package pluginmanager

import (
	"context"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestBuiltinCredentialPlanUsesPluginNamespace(t *testing.T) {
	ctx := t.Context()
	m, _, _ := queueManager(t)
	store, err := credentials.NewStore(ctx, m.main, m.authority, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.WithCredentials(store); err != nil {
		t.Fatal(err)
	}
	const id = "synthetic-external-plugin"
	const value = "synthetic-credential-value"
	policy := BuiltinPolicy{
		Prepare: func(_ context.Context, _, _ *jsjson.Object) (*jsjson.Object, []credentials.Change, error) {
			config, err := jsjson.NewObject(jsjson.Member{Key: "mode", Value: "fixture"})
			return config, []credentials.Change{{Namespace: "plugin:other", Name: "access", Mutation: &credentials.Mutation{Action: "set", Value: value}}}, err
		},
	}
	if err = m.writeBuiltinConfiguration(ctx, id, new(jsjson.Object), new(jsjson.Object), policy); err != nil {
		t.Fatal(err)
	}
	secret, found, err := store.Get(ctx, "plugin:"+id, "access")
	if err != nil || !found || secret != value {
		t.Fatal("plan for an arbitrary plugin did not store its own credential", found, err)
	}
	if found, err := store.Has(ctx, "plugin:other", "access"); err != nil || found {
		t.Fatal("plan escaped its namespace", found, err)
	}
	config, err := m.readConfig(ctx, id)
	if err != nil || prop(config, "mode") != "fixture" || prop(config, "access") != nil {
		t.Fatal("configuration did not commit separately from the credential", err)
	}
}
