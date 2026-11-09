package snowlumaruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
)

// Policy uses an immutable, separately loaded policy guest, not the adapter's
// enabled generation. Its caller owns the whole operation and physical lifetime.
func Policy(call func(context.Context, []byte) ([]byte, error), life context.Context, store *credentials.Store) pluginmanager.BuiltinPolicy {
	invoke := func(ctx context.Context, input *jsjson.Object) (*jsjson.Object, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		wire, e := jsjson.Marshal(input)
		if e != nil {
			return nil, e
		}
		raw, e := call(life, wire)
		if e != nil {
			return nil, e
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, e := jsjson.ParseBytes(raw)
		if e != nil {
			return nil, e
		}
		o, ok := value.(*jsjson.Object)
		if !ok {
			return nil, errors.New("invalid_guest_policy_result")
		}
		return o, nil
	}
	return pluginmanager.BuiltinPolicy{
		CredentialFailure: func(ctx context.Context, cause error) error {
			result, err := invoke(ctx, obj("operation", "configuration_applied", "failed", true, "errorIsError", true, "errorMessage", cause.Error()))
			if err != nil {
				return err
			}
			return pluginmanager.NewStartupError(str(result, "code"), nil)
		},
		Validate: func(ctx context.Context, value *jsjson.Object) error {
			result, e := invoke(ctx, obj("operation", "configuration_validate", "config", value))
			if e != nil {
				return e
			}
			if str(result, "kind") == "configuration" {
				return nil
			}
			issues := []pluginmanager.Issue{}
			if rows, ok := get(result, "issues").([]any); ok {
				for _, v := range rows {
					if row, ok := v.(*jsjson.Object); ok {
						issues = append(issues, pluginmanager.Issue{Field: str(row, "field"), Reason: str(row, "reason")})
					}
				}
			}
			return &pluginmanager.ConfigurationError{Issues: issues}
		},
		Prepare: func(ctx context.Context, value, previous *jsjson.Object) (*jsjson.Object, []credentials.Change, error) {
			input := obj("operation", "configuration_begin", "config", value, "previous", previous)
			changes := []credentials.Change{}
			for {
				result, e := invoke(ctx, input)
				if e != nil {
					return nil, nil, e
				}
				switch str(result, "kind") {
				case "configuration":
					config, ok := get(result, "config").(*jsjson.Object)
					if !ok {
						return nil, nil, errors.New("invalid_guest_configuration")
					}
					return config, changes, nil
				case "secret_apply":
					changes = append(changes, credentials.Change{Namespace: "plugin:snowluma", Name: str(result, "key"), Mutation: &credentials.Mutation{Action: str(result, "action"), Value: str(result, "value")}})
					// This is an ordered transaction plan, not an executed mutation. The
					// credential owner encrypts/commits in this order and stops on first error.
					input = obj("operation", "configuration_applied", "state", get(result, "state"))
				default:
					return nil, nil, errors.New("invalid_guest_configuration_step")
				}
			}
		},
		Secrets: func(ctx context.Context) (*jsjson.Object, error) {
			if store == nil {
				return nil, errors.New("plugin_credential_owner_missing")
			}
			input := obj("operation", "configuration_secrets")
			for {
				result, e := invoke(ctx, input)
				if e != nil {
					return nil, e
				}
				if str(result, "kind") == "configured_secrets" {
					value, ok := get(result, "secrets").(*jsjson.Object)
					if !ok {
						return nil, errors.New("invalid_guest_secret_result")
					}
					return value, nil
				}
				if str(result, "kind") != "secret_has" {
					return nil, errors.New("invalid_guest_secret_step")
				}
				present, e := store.Has(ctx, "plugin:snowluma", str(result, "key"))
				if e != nil {
					return nil, e
				}
				input = obj("operation", "configuration_secret_present", "state", get(result, "state"), "present", present)
			}
		},
	}
}
