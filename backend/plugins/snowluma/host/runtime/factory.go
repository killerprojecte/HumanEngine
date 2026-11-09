package snowlumaruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	transport "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/transport"
)

// Factory borrows one application Registry and the application's actual media,
// receive and stopped owners. The caller binds it once before RegisterBuiltin.
// No default/mock receive or media port can turn this into an enabled adapter.
func Factory(ports InstallationPorts, environment func(string) (string, bool)) pluginmanager.BuiltinRuntimeFactory {
	return func(ctx context.Context, in pluginmanager.BuiltinRuntimeInput) (pluginmanager.BuiltinLifetime, error) {
		value, err := jsjson.ParseBytes(in.Config)
		if err != nil {
			return nil, err
		}
		config, ok := value.(*jsjson.Object)
		if !ok {
			return nil, errors.New("snowluma_configuration_required")
		}
		wire, err := jsjson.Marshal(obj("operation", "configuration_validate", "config", config))
		if err != nil {
			return nil, err
		}
		raw, err := in.CallGuest(in.Life, wire)
		if err != nil {
			return nil, err
		}
		value, err = jsjson.ParseBytes(raw)
		if err != nil {
			return nil, err
		}
		validation, ok := value.(*jsjson.Object)
		if !ok {
			return nil, errors.New("snowluma_invalid_configuration_result")
		}
		if str(validation, "kind") != "configuration" {
			// Public fixed fields/reasons are produced in the guest. Do not rederive
			// schema diagnostics from native settings or leak submitted values.
			issues := []pluginmanager.Issue{}
			if rows, ok := get(validation, "issues").([]any); ok {
				for _, v := range rows {
					if row, ok := v.(*jsjson.Object); ok {
						issues = append(issues, pluginmanager.Issue{Field: str(row, "field"), Reason: str(row, "reason")})
					}
				}
			}
			return nil, &pluginmanager.ConfigurationError{Issues: issues}
		}
		parsed, ok := get(validation, "config").(*jsjson.Object)
		if !ok {
			return nil, errors.New("snowluma_invalid_configuration_result")
		}
		plan, err := PrepareStartup(ctx, in.Life, in.Current, in.CallGuest, parsed, StartupIO{Secret: in.Secret, Environment: environment})
		if err != nil {
			return nil, err
		}
		generation := ports
		generation.Sender.Life = in.Life
		generation.Sender.Current = in.Current
		generation.Status = func(status string) {
			if in.Life.Err() == nil && in.Current() && ports.Status != nil {
				ports.Status(status)
			}
		}
		generation.Error = func(code string, err error) {
			if in.Life.Err() == nil && in.Current() && ports.Error != nil {
				ports.Error(code, err)
			}
		}
		owner, err := InstallPrepared(ctx, in.Host, in.ID, plan, generation)
		var known *transport.Error
		if errors.As(err, &known) {
			err = pluginmanager.NewStartupError(known.Code, map[string]any{"httpStatus": known.HTTPStatus})
		}
		return owner, err
	}
}
