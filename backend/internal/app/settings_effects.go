package app

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/projections"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

// SettingsDomainOwners must be actual installed lifecycle owners. Their
// implementations are not supplied by this Meme-only composition.
type SettingsDomainOwners interface {
	LearningSettingsChanged(context.Context) error
	ConfigureSemantic(context.Context) error
}

type NativeSettingsEffects struct {
	projections *projections.Coordinator
	workspace   *workingset.Service
	maintenance *workingset.Maintenance
	domains     SettingsDomainOwners
	diagnostics *Diagnostics
	logs        *observability.Logger
	snapshot    *settingsSnapshot
}

func NewNativeSettingsEffects(p *projections.Coordinator, w *workingset.Service, m *workingset.Maintenance, domains SettingsDomainOwners, diagnostics *Diagnostics, logs *observability.Logger) (*NativeSettingsEffects, error) {
	if p == nil || w == nil || m == nil || domains == nil || diagnostics == nil || logs == nil {
		return nil, errors.New("application_settings_effect_owners_required")
	}
	return &NativeSettingsEffects{projections: p, workspace: w, maintenance: m, domains: domains, diagnostics: diagnostics, logs: logs}, nil
}
func (s *NativeSettingsEffects) FlushProjections(ctx context.Context) error {
	if _, err := s.projections.Flush(ctx); err != nil {
		// Source memory.flushProjections deliberately treats refresh failure as a
		// warning, not rollback/failure of already committed settings.
		emit(s.logs, observability.Event{Level: "warn", Module: "memory", Code: "memory_projection_refresh_failed", Message: "Memory projection refresh failed"})
	}
	return nil
}
func (s *NativeSettingsEffects) ConfigureWorkspace(ctx context.Context, settings map[string]any) error {
	cold := settings["memoryColdAfterDays"].(float64) * 86400000
	review := settings["memoryReviewAfterDays"].(float64) * 86400000
	if err := s.workspace.ConfigurePolicy(&cold, &review); err != nil {
		return err
	}
	if settings["memoryWorkingSetEnabled"] == false && settings["memoryEpisodesEnabled"] == false {
		return s.workspace.CancelMaintenance(ctx, nil)
	}
	return nil
}
func (s *NativeSettingsEffects) ConfigureMaintenance(context.Context) error {
	s.maintenance.Configure()
	return nil
}
func (s *NativeSettingsEffects) LearningSettingsChanged(ctx context.Context) error {
	return s.domains.LearningSettingsChanged(ctx)
}
func (s *NativeSettingsEffects) ConfigureSemantic(ctx context.Context) error {
	return s.domains.ConfigureSemantic(ctx)
}
func (s *NativeSettingsEffects) ClearDiagnostics(ctx context.Context) error {
	return s.diagnostics.ClearDiagnostics(ctx)
}

func (s *NativeSettingsEffects) PublishSettings(settings map[string]any) {
	s.diagnostics.PublishSettings(settings)
	if s.snapshot != nil {
		s.snapshot.publish(settings)
	}
}
