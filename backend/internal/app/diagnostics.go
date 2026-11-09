package app

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/modeldiagnostics"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"sync/atomic"
)

// Diagnostics binds ephemeral capture to the actual encrypted credential owner
// and fresh saved model provider list. It does not expose secret values to logs.
type Diagnostics struct {
	*modeldiagnostics.Store
	enabled   atomic.Bool
	main      *sql.DB
	authority *recovery.Authority
}

func NewDiagnostics(ctx context.Context, owners Owners, options Options) (*Diagnostics, error) {
	if owners.Main == nil || owners.Authority == nil || !owners.Credentials.OwnedBy(owners.Main, owners.Authority) {
		return nil, errors.New("application_diagnostic_owners_required")
	}
	config, err := readConfiguration(ctx, owners.Main, owners.Authority)
	if err != nil {
		return nil, err
	}
	d := &Diagnostics{main: owners.Main, authority: owners.Authority}
	d.enabled.Store(config.settings["debugModelIO"] == true)
	resolver := providertransport.StoredCredentials{Store: owners.Credentials, LookupEnv: options.LookupEnv}
	d.Store, err = modeldiagnostics.New(modeldiagnostics.Ports{Enabled: d.enabled.Load, Secrets: func(ctx context.Context) ([]string, error) {
		values, err := owners.Credentials.RedactionValues(ctx)
		if err != nil {
			return nil, err
		}
		config, err := readConfiguration(ctx, owners.Main, owners.Authority)
		if err != nil {
			return nil, err
		}
		for _, raw := range config.models["providers"].([]any) {
			p := raw.(map[string]any)
			value, err := resolver.Resolve(ctx, providertransport.Provider{ID: p["id"].(string), CredentialEnv: p["credentialEnv"].(string)})
			if err != nil {
				return nil, err
			}
			if value != "" {
				values = append(values, value)
			}
		}
		return values, nil
	}})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// PublishSettings mirrors the committed settings value becoming visible before
// Engine.settingsChanged executes its ordered side effects.
func (d *Diagnostics) PublishSettings(settings map[string]any) {
	d.enabled.Store(settings["debugModelIO"] == true)
}
func (d *Diagnostics) ClearDiagnostics(context.Context) error { d.Clear(); return nil }

func (d *Diagnostics) OwnedBy(main *sql.DB, authority *recovery.Authority) bool {
	return d != nil && d.main == main && d.authority == authority
}
