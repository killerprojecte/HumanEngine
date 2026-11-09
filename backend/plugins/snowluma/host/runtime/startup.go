package snowlumaruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
)

// StartupIO contains only scoped secret/environment reads. The config supplied
// here must be the guest schema parser result, never a native approximation.
type StartupIO struct {
	Secret      func(context.Context, string) (string, bool, error)
	Environment func(string) (string, bool)
}

func PrepareStartup(ctx context.Context, life context.Context, current func() bool, guest func(context.Context, []byte) ([]byte, error), parsedConfig *jsjson.Object, io StartupIO) (*jsjson.Object, error) {
	if life == nil || current == nil || guest == nil || io.Secret == nil || io.Environment == nil {
		return nil, errors.New("snowluma_startup_ports_incomplete")
	}
	input := obj("operation", "startup_begin", "config", parsedConfig)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if life.Err() != nil || !current() {
			return nil, errors.New("plugin_disabled")
		}
		encoded, err := jsjson.Stringify(input)
		if err != nil {
			return nil, err
		}
		// The bound guest execution belongs to the generation. A request cancellation
		// is checked outside the call and must not poison unrelated guest execution.
		raw, err := guest(life, []byte(encoded))
		if err != nil {
			return nil, err
		}
		value, err := jsjson.Parse(string(raw))
		if err != nil {
			return nil, err
		}
		decision, ok := value.(*jsjson.Object)
		if !ok {
			return nil, errors.New("invalid_guest_startup_result")
		}
		switch str(decision, "kind") {
		case "install":
			return decision, nil
		case "error":
			return nil, pluginmanager.NewStartupError(str(decision, "code"), nil)
		case "secret_get":
			value, present, e := io.Secret(ctx, str(decision, "key"))
			input = obj("operation", "startup_secret", "state", get(decision, "state"), "present", present, "value", value)
			if e != nil {
				_ = input.Set("failed", true)
				_ = input.Set("errorIsError", true)
				_ = input.Set("errorMessage", e.Error())
			}
		case "environment_get":
			value, present := io.Environment(str(decision, "name"))
			input = obj("operation", "startup_environment", "state", get(decision, "state"))
			if present {
				_ = input.Set("value", value)
			}
		default:
			return nil, errors.New("invalid_guest_startup_step")
		}
	}
}
