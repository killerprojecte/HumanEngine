// Package app composes the candidate native application. It is not installation
// enrollment and is deliberately not exposed by the production command.
package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/promptcache"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type configuration struct {
	settings map[string]any
	models   map[string]any
}

// Read one admitted snapshot. No provider credentials are included in this value.
func readConfiguration(ctx context.Context, db *sql.DB, a *recovery.Authority) (configuration, error) {
	var result configuration
	err := a.Publish(ctx, db, func(c *sql.Conn) ([]byte, error) {
		values := map[string]json.RawMessage{}
		for _, key := range []string{"settings", "models"} {
			var raw string
			err := c.QueryRowContext(ctx, "SELECT value FROM config WHERE key=?", key).Scan(&raw)
			if err == sql.ErrNoRows {
				if key == "settings" {
					raw = string(contract.DefaultSettingsInput())
				} else {
					raw = `{"providers":[],"models":[],"routes":[]}`
				}
			} else if err != nil {
				return nil, err
			}
			values[key] = json.RawMessage(raw)
		}
		models, err := credentials.ReadModelConfiguration(values["models"], func(namespace, name string) (bool, error) {
			var exists bool
			err := c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM secrets WHERE namespace=? AND name=?)", namespace, name).Scan(&exists)
			return exists, err
		})
		if err != nil {
			return nil, err
		}
		values["models"], err = jsjson.Marshal(models)
		if err != nil {
			return nil, err
		}
		return json.Marshal(values)
	}, func(raw []byte) error {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		schemas, err := contract.SharedSchemas()
		if err != nil {
			return err
		}
		result.settings, err = schemas.Parse("Settings", values["settings"])
		if err != nil {
			return errors.New("application_settings_invalid")
		}
		result.models, err = schemas.Parse("ModelConfig", values["models"])
		if err != nil {
			return errors.New("application_models_invalid")
		}
		return nil
	})
	return result, err
}

func runtimeSettings(v map[string]any) botruntime.Settings {
	n := func(k string) float64 { return v[k].(float64) }
	c := botruntime.DefaultSettings()
	c.Persona = v["persona"].(string)
	c.Timezone = v["timezone"].(string)
	c.CacheEnabled = v["cacheEnabled"].(bool)
	c.CacheHours = n("cacheHours")
	c.PromptContextWindowEnabled = v["promptContextWindowEnabled"].(bool)
	c.ReplySuggestionsEnabled = v["replySuggestionsEnabled"].(bool)
	c.TopicAdmissionEnabled = v["topicAdmissionEnabled"].(bool)
	c.DebounceMS = n("debounceMs")
	c.MaxWaitMS = n("maxWaitMs")
	c.GroupCooldownMS = n("groupTurnCooldownMs")
	c.DirectCooldownMS = n("directTurnCooldownMs")
	c.SceneConcurrency = int(n("sceneTurnConcurrency"))
	c.TopicHistoryMaxMessages = int(n("topicHistoryMaxMessages"))
	c.Window = promptcache.Options{MaxMessages: int(n("promptContextWindowMaxMessages")), RetainMessages: int(n("promptContextWindowRetainMessages")), MaxBytes: int(n("promptContextWindowMaxBytes"))}
	c.PlannerEvidenceMaxBytes = n("plannerEvidenceMaxBytes")
	c.ReplyEvidenceMaxBytes = n("replyEvidenceMaxBytes")
	// Leave Hour unset: botruntime.New supplies the native Intl provider with
	// its existing finite/range checks, rather than duplicating a weaker adapter.
	c.ParticipationWindows = nil
	for _, raw := range v["participationWindows"].([]any) {
		w := raw.(map[string]any)
		c.ParticipationWindows = append(c.ParticipationWindows, botruntime.ParticipationWindow{StartHour: int(w["startHour"].(float64)), EndHour: int(w["endHour"].(float64)), Inclination: w["inclination"].(string)})
	}
	return c
}

func modelConfiguration(v map[string]any, client *providertransport.Client, options modelservice.Options) (*providertransport.NativeModels, error) {
	// Schema validation already produced canonical WTF-8 strings. Map fields
	// directly: encoding/json's replacement of lone surrogates is not lossless.
	var config modelservice.Config
	stringsOf := func(v any) []string {
		values, _ := v.([]any)
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = v.(string)
		}
		return out
	}
	numberOf := func(v any) *float64 {
		if n, ok := v.(float64); ok {
			return &n
		}
		return nil
	}
	for _, raw := range v["providers"].([]any) {
		p := raw.(map[string]any)
		config.Providers = append(config.Providers, modelservice.Provider{ID: p["id"].(string), Protocol: p["protocol"].(string), RetryCount: numberOf(p["retryCount"]), TimeoutMS: numberOf(p["timeoutMs"])})
	}
	for _, raw := range v["models"].([]any) {
		p := raw.(map[string]any)
		var streaming *bool
		if b, ok := p["streaming"].(bool); ok {
			streaming = &b
		}
		config.Models = append(config.Models, modelservice.Model{ID: p["id"].(string), ProviderID: p["providerId"].(string), APIModelName: p["apiModelName"].(string), Capabilities: stringsOf(p["capabilities"]), Streaming: streaming})
	}
	for _, raw := range v["routes"].([]any) {
		p := raw.(map[string]any)
		config.Routes = append(config.Routes, modelservice.Route{Role: p["role"].(string), OrderedModelIDs: stringsOf(p["orderedModelIds"]), TimeoutMS: numberOf(p["timeoutMs"])})
	}
	if client.MaxOutputTokens != nil {
		n := float64(*client.MaxOutputTokens)
		config.NativeMaxOutputTokens = &n
	}
	policy, err := modelservice.New(config, options)
	if err != nil {
		return nil, err
	}
	m := &providertransport.NativeModels{Policy: policy, Client: client, Providers: map[string]providertransport.Provider{}, ProviderSnapshots: map[string]map[string]any{}, ModelSnapshots: map[string]map[string]any{}, Prices: map[string]map[string]float64{}, CacheModes: map[string]string{}}
	for _, raw := range v["providers"].([]any) {
		p := raw.(map[string]any)
		id := p["id"].(string)
		provider := providertransport.Provider{ID: id, Protocol: p["protocol"].(string), BaseURL: p["baseUrl"].(string), CredentialEnv: p["credentialEnv"].(string)}
		provider.CredentialConfigured, _ = p["credentialConfigured"].(bool)
		if limits, ok := p["imageUploadLimits"]; ok {
			b, e := json.Marshal(limits)
			if e != nil {
				return nil, e
			}
			if e = json.Unmarshal(b, &provider.ImageUploadLimits); e != nil {
				return nil, e
			}
		}
		m.Providers[id] = provider
		m.ProviderSnapshots[id] = p
	}
	for _, raw := range v["models"].([]any) {
		model := raw.(map[string]any)
		id := model["id"].(string)
		m.ModelSnapshots[id] = model
		m.CacheModes[id], _ = model["promptCacheMode"].(string)
		prices := map[string]float64{}
		for _, key := range []string{"inputUsdPerMillion", "outputUsdPerMillion", "cacheReadUsdPerMillion", "cacheWriteUsdPerMillion", "cacheWrite5mUsdPerMillion", "cacheWrite1hUsdPerMillion", "cacheWrite30mUsdPerMillion", "cacheWrite12hUsdPerMillion"} {
			if n, ok := model[key].(float64); ok {
				prices[key] = n
			}
		}
		m.Prices[id] = prices
	}
	return m, nil
}
