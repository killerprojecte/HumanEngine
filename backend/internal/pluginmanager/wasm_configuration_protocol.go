package pluginmanager

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/credentials"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// configurationPolicy uses an immutable, separately loaded policy guest, not the adapter's
// enabled generation. Its caller owns the whole operation and physical lifetime.
func configurationPolicy(call func(context.Context, []byte) ([]byte, error), life context.Context, hasSecret func(context.Context, string) (bool, error)) BuiltinPolicy {
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
	return BuiltinPolicy{
		CredentialFailure: func(ctx context.Context, cause error) error {
			result, err := invoke(ctx, policyObject("operation", "configuration_applied", "failed", true, "errorIsError", true, "errorMessage", cause.Error()))
			if err != nil {
				return err
			}
			return NewStartupError(text(result, "code"), nil)
		},
		Validate: func(ctx context.Context, value *jsjson.Object) error {
			result, e := invoke(ctx, policyObject("operation", "configuration_validate", "config", value))
			if e != nil {
				return e
			}
			if text(result, "kind") == "configuration" {
				return nil
			}
			issues := []Issue{}
			if rows, ok := prop(result, "issues").([]any); ok {
				for _, v := range rows {
					if row, ok := v.(*jsjson.Object); ok {
						issues = append(issues, Issue{Field: text(row, "field"), Reason: text(row, "reason")})
					}
				}
			}
			return &ConfigurationError{Issues: issues}
		},
		Prepare: func(ctx context.Context, value, previous *jsjson.Object) (*jsjson.Object, []credentials.Change, error) {
			input := policyObject("operation", "configuration_begin", "config", value, "previous", previous)
			changes := []credentials.Change{}
			for {
				result, e := invoke(ctx, input)
				if e != nil {
					return nil, nil, e
				}
				switch text(result, "kind") {
				case "configuration":
					config, ok := prop(result, "config").(*jsjson.Object)
					if !ok {
						return nil, nil, errors.New("invalid_guest_configuration")
					}
					return config, changes, nil
				case "secret_apply":
					changes = append(changes, credentials.Change{Name: text(result, "key"), Mutation: &credentials.Mutation{Action: text(result, "action"), Value: text(result, "value")}})
					// This is an ordered transaction plan, not an executed mutation. The
					// credential owner encrypts/commits in this order and stops on first error.
					input = policyObject("operation", "configuration_applied", "state", prop(result, "state"))
				default:
					return nil, nil, errors.New("invalid_guest_configuration_step")
				}
			}
		},
		Secrets: func(ctx context.Context) (*jsjson.Object, error) {
			if hasSecret == nil {
				return nil, errors.New("plugin_credential_owner_missing")
			}
			input := policyObject("operation", "configuration_secrets")
			for {
				result, e := invoke(ctx, input)
				if e != nil {
					return nil, e
				}
				if text(result, "kind") == "configured_secrets" {
					value, ok := prop(result, "secrets").(*jsjson.Object)
					if !ok {
						return nil, errors.New("invalid_guest_secret_result")
					}
					return value, nil
				}
				if text(result, "kind") != "secret_has" {
					return nil, errors.New("invalid_guest_secret_step")
				}
				present, e := hasSecret(ctx, text(result, "key"))
				if e != nil {
					return nil, e
				}
				input = policyObject("operation", "configuration_secret_present", "state", prop(result, "state"), "present", present)
			}
		},
	}
}

func policyObject(pairs ...any) *jsjson.Object {
	o, _ := jsjson.NewObject()
	for i := 0; i < len(pairs); i += 2 {
		_ = o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}
