package app

import (
	"context"
	"errors"
	"sync"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/httpapi"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/scheduler"
)

// SettingsEffects are the remaining real Engine owners. This composition does
// not create successful stand-ins for domains that have not been ported. Each
// method must finish its synchronous source effect before returning; background
// work that it creates remains owned by that domain's shutdown barrier.
type SettingsEffects interface {
	PublishSettings(map[string]any)
	FlushProjections(context.Context) error
	LearningSettingsChanged(context.Context) error
	ConfigureWorkspace(context.Context, map[string]any) error
	ConfigureMaintenance(context.Context) error
	ConfigureSemantic(context.Context) error
	ClearDiagnostics(context.Context) error
}

type SettingsRuntime struct {
	mu        sync.Mutex
	runtime   *botruntime.Runtime
	scheduler *scheduler.Scheduler
	effects   SettingsEffects
}

func NewSettingsRuntime(runtime *botruntime.Runtime, scheduler *scheduler.Scheduler, effects SettingsEffects) (*SettingsRuntime, error) {
	if runtime == nil || scheduler == nil || effects == nil {
		return nil, errors.New("application_settings_owners_required")
	}
	return &SettingsRuntime{runtime: runtime, scheduler: scheduler, effects: effects}, nil
}

// SettingsChanged preserves engine.ts's post-commit order. The HTTP owner keeps
// this call inside its accepted request lifetime; a disconnected client cannot
// abandon already committed effects. Any failure is reported, never rolled back
// or presented as a successful application of all saved settings.
func (s *SettingsRuntime) SettingsChanged(ctx context.Context, change httpapi.SettingsChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx = context.WithoutCancel(ctx)
	parse := func(value *jsjson.Object) (map[string]any, error) {
		if value == nil {
			return nil, errors.New("application_settings_snapshot_required")
		}
		raw, err := jsjson.Marshal(value)
		if err != nil {
			return nil, err
		}
		schemas, err := contract.SharedSchemas()
		if err != nil {
			return nil, err
		}
		return schemas.Parse("Settings", raw)
	}
	previous, err := parse(change.Previous)
	if err != nil {
		return err
	}
	current, err := parse(change.Current)
	if err != nil {
		return err
	}
	s.effects.PublishSettings(current)
	if previous["topicAdmissionEnabled"] != current["topicAdmissionEnabled"] {
		if err = s.runtime.ClearDeferredTopics(); err != nil {
			return err
		}
	}
	for _, effect := range []func(context.Context) error{s.effects.FlushProjections, s.effects.LearningSettingsChanged, func(ctx context.Context) error { return s.effects.ConfigureWorkspace(ctx, current) }, s.effects.ConfigureMaintenance, s.effects.ConfigureSemantic} {
		if err = effect(ctx); err != nil {
			return err
		}
	}
	if err = s.scheduler.Configure(current["modelConcurrency"].(float64), current["modelCallsPerMinute"].(float64)); err != nil {
		return err
	}
	if err = s.runtime.ConfigureAdmission(runtimeSettings(current)); err != nil {
		return err
	}
	if current["debugModelIO"] == false {
		return s.effects.ClearDiagnostics(ctx)
	}
	return nil
}
