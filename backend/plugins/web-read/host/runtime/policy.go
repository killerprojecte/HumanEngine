//go:build web_read_candidate

package webreadruntime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
)

// Policy uses a separately captured guest generation, including while the
// service is disabled. Queue cancellation is distinct from synchronous policy.
func Policy(call func(context.Context, context.Context, []byte) ([]byte, error), life context.Context) pluginmanager.BuiltinPolicy {
	return pluginmanager.BuiltinPolicy{
		Validate: func(ctx context.Context, input *jsjson.Object) error {
			config, err := jsjson.Marshal(input)
			if err != nil {
				return err
			}
			wire, err := json.Marshal(map[string]any{"operation": "configurationValidate", "config": json.RawMessage(config)})
			if err != nil {
				return err
			}
			raw, err := call(ctx, life, wire)
			if err != nil {
				return err
			}
			var result struct {
				Valid *bool `json:"valid"`
			}
			if err = json.Unmarshal(raw, &result); err != nil {
				return err
			}
			if result.Valid == nil {
				return errors.New("web_read_policy_artifact_mismatch")
			}
			if !*result.Valid {
				return &pluginmanager.ConfigurationError{Issues: []pluginmanager.Issue{}}
			}
			return nil
		},
		// Source has no prepareConfiguration hook. Defaults apply in setup, not to
		// persisted raw configuration, and web-read has no credential mutations.
		Prepare: func(_ context.Context, input, _ *jsjson.Object) (*jsjson.Object, []credentials.Change, error) {
			return input, nil, nil
		},
		Secrets: func(context.Context) (*jsjson.Object, error) { return new(jsjson.Object), nil },
	}
}
