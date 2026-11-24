package app

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	snow "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/runtime"
	"os"
	"time"
)

// bindSnowLuma owns the application's sole Snow registry. Policy inspection is
// a temporary independently bound WASM module for each complete explicit policy
// operation. No policy guest remains loaded while idle or after plugin disable.
func (a *Application) bindSnowLuma(ctx context.Context, owners Owners) error {
	if a.snowRegistry != nil {
		return errors.New("snowluma_application_already_bound")
	}
	repository, err := ingressrouting.NewSQLiteRepository(owners.Main, owners.Authority)
	if err != nil {
		return err
	}
	identity, err := ingressrouting.New(repository)
	if err != nil {
		return err
	}
	artifact, err := plugins.BuiltinArtifact("snowluma")
	if err != nil {
		return err
	}
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
	if err = a.registerBuiltin("snowluma", snow.Factory(ports, lookup), snow.TransientPolicy(a.WASM, "snowluma-policy", artifact, hasSecret)); err != nil {
		return err
	}
	a.snowRegistry = registry
	return nil
}
