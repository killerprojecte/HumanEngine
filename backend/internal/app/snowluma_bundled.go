//go:build bundled_wasm

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	resource "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/runtime"
)

func distributionBuiltins() []string { return []string{"typing-delay", "snowluma"} }

// Only the compatibility distribution statically links the legacy Snow host.
func (a *Application) bindDistributionBuiltin(ctx context.Context, owners Owners, id string) error {
	if id != "snowluma" {
		return errors.New("builtin_runtime_not_implemented")
	}
	manifest, ok := resource.Manifest().(json.RawMessage)
	if !ok {
		return errors.New("builtin_manifest_invalid")
	}
	repository, err := ingressrouting.NewSQLiteRepository(owners.Main, owners.Authority)
	if err != nil {
		return err
	}
	identity, err := ingressrouting.New(repository)
	if err != nil {
		return err
	}
	artifact, err := resource.Artifact()
	if err != nil {
		return err
	}
	loadArtifact := func() ([]byte, error) { return artifact, nil }
	hasSecret := func(ctx context.Context, name string) (bool, error) {
		return owners.Credentials.Has(ctx, "plugin:snowluma", name)
	}
	registry := snow.NewRegistry(a.Engine.Runtime)
	ingress := snow.EngineIngress{Engine: a.Engine, Identity: identity, Compare: owners.Compare}
	log := func(level, code string) {
		emit(a.options.Logs, observability.Event{Level: level, Module: "snowluma", Code: code, Message: code})
	}
	ports := snow.InstallationPorts{Registry: registry, Sender: snow.Ports{ReadOriginal: snow.OriginalReader(a.Engine)}, BuildReceive: ingress.ReceiverFactory(func() { log("warn", "profile_lookup_unavailable") }, time.Now), Stopped: a.cancelAdapterScenes, Status: func(status string) { log("info", status) }, Error: func(code string, _ error) { log("error", code) }}
	lookup := a.options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return a.Plugins.RegisterBuiltin(pluginmanager.BuiltinDescriptor{
		Manifest: manifest, LoadArtifact: loadArtifact,
		Start:  snow.Factory(ports, lookup),
		Policy: pluginmanager.WASMConfigurationPolicy(a.WASM, id, loadArtifact, hasSecret),
	})
}
