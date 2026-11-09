package app

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
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
	if err = a.Plugins.WithBuiltinPolicy("snowluma", snow.TransientPolicy(a.WASM, "snowluma-policy", artifact, owners.Credentials)); err != nil {
		return err
	}
	registry := snow.NewRegistry(a.Engine.Runtime)
	ingress := snow.EngineIngress{Engine: a.Engine, Identity: identity, Compare: owners.Compare}
	log := func(level, code string) {
		emit(a.options.Logs, observability.Event{Level: level, Module: "snowluma", Code: code, Message: code})
	}
	ports := snow.InstallationPorts{Registry: registry, Sender: snow.Ports{ReadOriginal: snow.OriginalReader(a.Engine)}, BuildReceive: ingress.ReceiverFactory(func() { log("warn", "profile_lookup_unavailable") }, time.Now), Stopped: a.snowStopped, Status: func(status string) { log("info", status) }, Error: func(code string, _ error) { log("error", code) }}
	lookup := a.options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if err = a.Plugins.WithBuiltinRuntime("snowluma", snow.Factory(ports, lookup)); err != nil {
		return err
	}
	a.snowRegistry = registry
	return nil
}
func (a *Application) snowStopped() error {
	root, release, err := a.Engine.RootContext(context.Background())
	if err != nil {
		return err
	}
	defer release()
	scenes := []string{}
	err = a.Engine.Sources.WithRuntimeReads(root, func(reads botruntime.ReadAuthority) error {
		return a.Engine.Sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			rows, err := scope.Query("SELECT value FROM scenes")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var raw string
				if err = rows.Scan(&raw); err != nil {
					return err
				}
				value, e := jsjson.Parse(raw)
				if e != nil {
					return e
				}
				if scene, ok := value.(*jsjson.Object); ok {
					adapter, _ := scene.Get("adapterId")
					if adapter == "snowluma" {
						id, _ := scene.Get("id")
						if text, ok := id.(string); ok {
							scenes = append(scenes, text)
						}
					}
				}
			}
			return rows.Err()
		})
	})
	if err != nil {
		return err
	}
	for _, id := range scenes {
		a.Learning.Coordinator.CancelScene(id)
		a.Semantic.CancelScene(id)
		a.Contextual.CancelScene(id)
		a.MediaTasks.CancelScene(id)
	}
	return nil
}
